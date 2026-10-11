package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"theophylax/internal/guardrails"
	"theophylax/internal/providers"
	"theophylax/internal/secrets"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

// Snapshot is an immutable, in-memory view of routing and policy config.
// The hot path never touches the database for config; snapshots are rebuilt
// on Postgres NOTIFY (instant, cross-replica) and on a periodic timer.
type Snapshot struct {
	Models    map[string]*Route
	Wildcards []*Route
	Providers map[string]*ProviderEntry
	Policies  map[string][]guardrails.Policy // "org:<id>", "team:<id>", "project:<id>"
	Budgets   map[string][]*store.Budget     // "<scope_type>:<id>"
	Orgs      map[string]*ScopeInfo
	Teams     map[string]*ScopeInfo
	Projects  map[string]*ScopeInfo
	Agents    map[string]*store.Agent // by id and "<org>/<slug>"
	Registry  map[string]*RegistryEntry
	IdPs      []*store.IdP
	Rollouts  map[string]*activeRollout // by model id
	Loaded    time.Time
}

type ScopeInfo struct {
	ID, OrgID, TeamID, Slug, Name string
	Settings                      store.ScopeSettings
}

type RegistryEntry struct {
	Item    *store.RegistryItem
	Version string
	Content map[string]any
}

type ProviderEntry struct {
	Row *store.Provider
	P   providers.Provider
	Err string
}

type Route struct {
	Model *store.Model
	Deps  []*Dep
}

type Dep struct {
	*store.Deployment
	Provider *ProviderEntry
	health   *health
}

type health struct {
	mu            sync.Mutex
	cooldownUntil time.Time
	fails         int
	ewmaMS        float64
}

func (h *health) available() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return time.Now().After(h.cooldownUntil)
}

func (h *health) fail() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fails++
	d := time.Duration(1<<min(h.fails, 6)) * time.Second // 2s .. 64s
	h.cooldownUntil = time.Now().Add(d)
}

func (h *health) success(lat time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fails = 0
	ms := float64(lat.Milliseconds())
	if h.ewmaMS == 0 {
		h.ewmaMS = ms
	} else {
		h.ewmaMS = 0.8*h.ewmaMS + 0.2*ms
	}
}

func (h *health) latency() float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ewmaMS == 0 {
		return 0 // unmeasured deployments are explored first
	}
	return h.ewmaMS
}

// Resolve finds a route for a requested model name or alias (wildcards last).
func (s *Snapshot) Resolve(name string) *Route {
	if r, ok := s.Models[strings.ToLower(name)]; ok {
		return r
	}
	best := (*Route)(nil)
	for _, r := range s.Wildcards {
		if util.MatchModel(strings.ToLower(r.Model.Name), strings.ToLower(name)) {
			if best == nil || len(r.Model.Name) > len(best.Model.Name) {
				best = r
			}
		}
	}
	return best
}

// Order returns candidate deployments for an org according to the model's strategy.
func (r *Route) Order(orgID string) []*Dep {
	var c []*Dep
	for _, d := range r.Deps {
		if !d.Enabled || d.Provider == nil || d.Provider.P == nil || !d.Provider.Row.Enabled {
			continue
		}
		if d.Provider.Row.OrgID != nil && *d.Provider.Row.OrgID != orgID {
			continue
		}
		c = append(c, d)
	}
	weightedShuffle := func(ds []*Dep) {
		// Efraimidis–Spirakis weighted random ordering.
		keys := make(map[*Dep]float64, len(ds))
		for _, d := range ds {
			w := float64(max(d.Weight, 1))
			keys[d] = -rand.ExpFloat64() / w
		}
		sort.SliceStable(ds, func(i, j int) bool { return keys[ds[i]] > keys[ds[j]] })
	}
	switch r.Model.Strategy {
	case "weighted":
		weightedShuffle(c)
	case "latency":
		rand.Shuffle(len(c), func(i, j int) { c[i], c[j] = c[j], c[i] })
		sort.SliceStable(c, func(i, j int) bool { return c[i].health.latency() < c[j].health.latency() })
	default: // priority, weighted within equal priority
		weightedShuffle(c)
		sort.SliceStable(c, func(i, j int) bool { return c[i].Priority < c[j].Priority })
	}
	// Healthy first; cooled-down deployments are kept as a last resort.
	sort.SliceStable(c, func(i, j int) bool { return c[i].health.available() && !c[j].health.available() })
	return c
}

func (d *Dep) Upstream(requested string) string {
	return strings.ReplaceAll(d.UpstreamModel, "{model}", requested)
}

// PoliciesFor returns policies in evaluation order: org, team, project.
func (s *Snapshot) PoliciesFor(orgID, teamID, projectID string) []guardrails.Policy {
	var out []guardrails.Policy
	out = append(out, s.Policies["org:"+orgID]...)
	if teamID != "" {
		out = append(out, s.Policies["team:"+teamID]...)
	}
	if projectID != "" {
		out = append(out, s.Policies["project:"+projectID]...)
	}
	return out
}

// ---------------------------------------------------------------------------

type Loader struct {
	st      *store.Store
	box     *secrets.Box
	timeout time.Duration
	cur     atomic.Pointer[Snapshot]
	mu      sync.Mutex
	provs   map[string]*cachedProvider
	// loadMu serialises Load. Without it, a reload that read the database
	// before a change committed can finish after one that read it afterwards
	// and replace the newer snapshot with a stale one.
	loadMu  sync.Mutex
	healths sync.Map // deployment id -> *health
	kick    chan struct{}
}

type cachedProvider struct {
	updated time.Time
	entry   *ProviderEntry
}

func NewLoader(st *store.Store, box *secrets.Box, timeout time.Duration) *Loader {
	return &Loader{st: st, box: box, timeout: timeout, provs: map[string]*cachedProvider{}, kick: make(chan struct{}, 1)}
}

func (l *Loader) Snapshot() *Snapshot { return l.cur.Load() }

// Kick schedules a reload (local replica).
func (l *Loader) Kick() {
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

func parseSettings(raw json.RawMessage) store.ScopeSettings {
	var s store.ScopeSettings
	_ = json.Unmarshal(raw, &s)
	return s
}

func (l *Loader) Load(ctx context.Context) error {
	l.loadMu.Lock()
	defer l.loadMu.Unlock()
	q := l.st.Pool
	s := &Snapshot{Models: map[string]*Route{}, Providers: map[string]*ProviderEntry{}, Policies: map[string][]guardrails.Policy{},
		Budgets: map[string][]*store.Budget{}, Orgs: map[string]*ScopeInfo{}, Teams: map[string]*ScopeInfo{}, Projects: map[string]*ScopeInfo{},
		Agents: map[string]*store.Agent{}, Registry: map[string]*RegistryEntry{}, Loaded: time.Now()}

	provs, err := store.List[store.Provider](ctx, q, `SELECT * FROM providers`)
	if err != nil {
		return err
	}
	l.mu.Lock()
	seen := map[string]bool{}
	for i := range provs {
		p := &provs[i]
		seen[p.ID] = true
		if c, ok := l.provs[p.ID]; ok && c.updated.Equal(p.UpdatedAt) {
			// Snapshots are immutable once published: requests still running on
			// the previous snapshot read this entry, so publish a copy with the
			// fresh row instead of mutating the shared one. The provider client
			// itself is reused.
			ne := *c.entry
			ne.Row = p
			c.entry = &ne
			s.Providers[p.ID] = c.entry
			continue
		}
		e := &ProviderEntry{Row: p}
		conn, err := l.box.OpenString(p.ConnEnc)
		if err != nil {
			e.Err = "cannot decrypt connection string (master key changed?)"
		} else {
			var pc struct {
				DropParams     []string          `json:"drop_params"`
				Headers        map[string]string `json:"headers"`
				TimeoutSeconds int               `json:"timeout_seconds"`
			}
			_ = json.Unmarshal(p.Config, &pc)
			to := l.timeout
			if pc.TimeoutSeconds > 0 {
				to = time.Duration(pc.TimeoutSeconds) * time.Second
			}
			pp, err := providers.New(conn, providers.Options{ID: p.ID, Timeout: to, DropParams: pc.DropParams, Headers: pc.Headers, Tokens: &tokenStore{st: l.st, box: l.box}})
			if err != nil {
				e.Err = err.Error()
			} else {
				e.P = pp
			}
		}
		if e.Err != "" {
			slog.Warn("provider unavailable", "provider", p.Name, "err", e.Err)
		}
		l.provs[p.ID] = &cachedProvider{updated: p.UpdatedAt, entry: e}
		s.Providers[p.ID] = e
	}
	for id := range l.provs {
		if !seen[id] {
			delete(l.provs, id)
		}
	}
	l.mu.Unlock()

	models, err := store.List[store.Model](ctx, q, `SELECT * FROM models WHERE status <> 'disabled'`)
	if err != nil {
		return err
	}
	deps, err := store.List[store.Deployment](ctx, q, `SELECT * FROM deployments`)
	if err != nil {
		return err
	}
	routes := map[string]*Route{}
	for i := range models {
		m := &models[i]
		r := &Route{Model: m}
		routes[m.ID] = r
		if strings.Contains(m.Name, "*") {
			s.Wildcards = append(s.Wildcards, r)
			continue
		}
		s.Models[strings.ToLower(m.Name)] = r
		for _, a := range m.Aliases {
			s.Models[strings.ToLower(a)] = r
		}
	}
	for i := range deps {
		d := &deps[i]
		r := routes[d.ModelID]
		if r == nil {
			continue
		}
		h, _ := l.healths.LoadOrStore(d.ID, &health{})
		r.Deps = append(r.Deps, &Dep{Deployment: d, Provider: s.Providers[d.ProviderID], health: h.(*health)})
	}

	pols, err := store.List[store.GuardrailPolicy](ctx, q, `SELECT * FROM guardrail_policies WHERE enabled ORDER BY created_at`)
	if err != nil {
		return err
	}
	for _, p := range pols {
		rules, err := guardrails.ParseRules(p.Rules)
		if err != nil {
			slog.Warn("invalid guardrail policy skipped", "policy", p.Name, "err", err)
			continue
		}
		k := p.ScopeType + ":" + p.ScopeID
		dg := sha256.Sum256(append([]byte(p.Mode+"|"), p.Rules...))
		s.Policies[k] = append(s.Policies[k], guardrails.Policy{ID: p.ID, Name: p.Name, Mode: p.Mode, ScopeType: p.ScopeType, Rules: rules, Digest: "sha256:" + hex.EncodeToString(dg[:])})
	}

	buds, err := store.List[store.Budget](ctx, q, `SELECT * FROM budgets`)
	if err != nil {
		return err
	}
	for i := range buds {
		b := &buds[i]
		k := b.ScopeType + ":" + b.ScopeID
		s.Budgets[k] = append(s.Budgets[k], b)
	}

	orgs, err := store.List[store.Org](ctx, q, `SELECT * FROM orgs`)
	if err != nil {
		return err
	}
	for _, o := range orgs {
		s.Orgs[o.ID] = &ScopeInfo{ID: o.ID, OrgID: o.ID, Slug: o.Slug, Name: o.Name, Settings: parseSettings(o.Settings)}
	}
	teams, err := store.List[store.Team](ctx, q, `SELECT * FROM teams`)
	if err != nil {
		return err
	}
	for _, t := range teams {
		s.Teams[t.ID] = &ScopeInfo{ID: t.ID, OrgID: t.OrgID, TeamID: t.ID, Slug: t.Slug, Name: t.Name, Settings: parseSettings(t.Settings)}
	}
	projs, err := store.List[store.Project](ctx, q, `SELECT * FROM projects`)
	if err != nil {
		return err
	}
	for _, p := range projs {
		s.Projects[p.ID] = &ScopeInfo{ID: p.ID, OrgID: p.OrgID, TeamID: p.TeamID, Slug: p.Slug, Name: p.Name, Settings: parseSettings(p.Settings)}
	}

	agents, err := store.List[store.Agent](ctx, q, `SELECT * FROM agents`)
	if err != nil {
		return err
	}
	for i := range agents {
		a := &agents[i]
		s.Agents[a.ID] = a
		s.Agents[a.OrgID+"/"+a.Slug] = a
	}

	type regRow struct {
		store.RegistryItem
		Content json.RawMessage `db:"content"`
	}
	regs, err := store.List[regRow](ctx, q, `SELECT i.*, v.content FROM registry_items i JOIN registry_versions v ON v.item_id=i.id AND v.version=i.approved_version WHERE i.approved_version<>'' AND i.status<>'deprecated' AND i.kind IN ('skill','mcp_server','prompt')`)
	if err != nil {
		return err
	}
	for i := range regs {
		var c map[string]any
		_ = json.Unmarshal(regs[i].Content, &c)
		item := regs[i].RegistryItem
		if enc, _ := c["authorization_token_enc"].(string); enc != "" {
			if raw, err := base64.StdEncoding.DecodeString(enc); err == nil {
				if tok, err := l.box.OpenString(raw); err == nil {
					c["authorization_token"] = tok
				}
			}
		}
		s.Registry[item.ID] = &RegistryEntry{Item: &item, Version: item.ApprovedVersion, Content: c}
	}

	idps, err := store.List[store.IdP](ctx, q, `SELECT * FROM idp_providers WHERE enabled AND gateway_tokens`)
	if err != nil {
		return err
	}
	for i := range idps {
		s.IdPs = append(s.IdPs, &idps[i])
	}
	if err := l.loadRollouts(ctx, s); err != nil {
		return err
	}
	l.cur.Store(s)
	return nil
}

// Run keeps the snapshot fresh: LISTEN for config changes, plus a periodic safety reload.
func (l *Loader) Run(ctx context.Context, pool *pgxpool.Pool) {
	notify := make(chan struct{}, 1)
	go func() {
		for ctx.Err() == nil {
			conn, err := pool.Acquire(ctx)
			if err != nil {
				time.Sleep(2 * time.Second)
				continue
			}
			if _, err := conn.Exec(ctx, "LISTEN theophylax_config"); err != nil {
				conn.Release()
				time.Sleep(2 * time.Second)
				continue
			}
			for {
				if _, err := conn.Conn().WaitForNotification(ctx); err != nil {
					break
				}
				select {
				case notify <- struct{}{}:
				default:
				}
			}
			conn.Release()
		}
	}()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-notify:
		case <-l.kick:
		}
		time.Sleep(100 * time.Millisecond) // debounce bursts of changes
		if err := l.Load(ctx); err != nil && ctx.Err() == nil {
			slog.Error("config reload failed", "err", err)
		}
	}
}

// NotifyChange tells every replica to reload.
func NotifyChange(ctx context.Context, pool *pgxpool.Pool) {
	_, _ = pool.Exec(ctx, "SELECT pg_notify('theophylax_config', '')")
}
