package api

import (
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"time"

	"theophylax/internal/alerts"
	"theophylax/internal/auth"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

// Alert notification channels (org admins). Targets are encrypted at rest and
// never returned in full: URLs are shown as their host only.

type channelView struct {
	store.NotificationChannel
	Target    string `json:"target"` // masked
	HasSecret bool   `json:"has_secret"`
}

type channelIn struct {
	Name    *string   `json:"name"`
	Type    *string   `json:"type"`
	Target  *string   `json:"target"`
	Secret  *string   `json:"secret"`
	Events  *[]string `json:"events"`
	Enabled *bool     `json:"enabled"`
}

func (a *API) channelView(ch store.NotificationChannel) channelView {
	v := channelView{NotificationChannel: ch, HasSecret: len(ch.SecretEnc) > 0}
	if v.Events == nil {
		v.Events = []string{}
	}
	if raw, err := a.Box.OpenString(ch.TargetEnc); err == nil {
		if ch.Type == "email" {
			v.Target = raw
		} else if u, err := url.Parse(raw); err == nil {
			v.Target = u.Scheme + "://" + u.Host + "/…"
		}
	}
	return v
}

func (a *API) listChannels(c *Ctx) (any, error) {
	o, _, err := a.orgCtx(c, "admin")
	if err != nil {
		return nil, err
	}
	list, err := store.List[store.NotificationChannel](c, a.St.Pool, `SELECT * FROM notification_channels WHERE org_id=$1 ORDER BY created_at`, o.ID)
	if err != nil {
		return nil, err
	}
	out := make([]channelView, 0, len(list))
	for _, ch := range list {
		out = append(out, a.channelView(ch))
	}
	return map[string]any{"channels": out, "event_types": alerts.EventTypes, "email_configured": a.Alerts != nil && a.Alerts.Deliver.SMTP != nil}, nil
}

func (a *API) applyChannel(ch *store.NotificationChannel, in *channelIn) error {
	if in.Name != nil {
		ch.Name = strings.TrimSpace(*in.Name)
	}
	if in.Type != nil {
		ch.Type = *in.Type
	}
	switch ch.Type {
	case "slack", "teams", "webhook", "email":
	default:
		return badReq("type must be slack, teams, webhook or email")
	}
	if ch.Type == "email" && (a.Alerts == nil || a.Alerts.Deliver.SMTP == nil) {
		return badReq("email channels need THEOPHYLAX_SMTP_URL to be configured on the gateway")
	}
	if in.Target != nil {
		t := strings.TrimSpace(*in.Target)
		if ch.Type == "email" {
			var addrs []string
			for _, s := range strings.Split(t, ",") {
				s = strings.TrimSpace(s)
				if s == "" {
					continue
				}
				if _, err := mail.ParseAddress(s); err != nil || strings.ContainsAny(s, "<>\r\n") {
					return badReq("target must be a comma-separated list of email addresses")
				}
				addrs = append(addrs, s)
			}
			if len(addrs) == 0 {
				return badReq("target must contain at least one email address")
			}
			t = strings.Join(addrs, ", ")
		} else {
			u, err := url.Parse(t)
			if err != nil || u.Host == "" || (u.Scheme != "https" && !(a.Cfg.DevMode && u.Scheme == "http")) {
				return badReq("target must be an https:// URL")
			}
		}
		ch.TargetEnc = a.Box.SealString(t)
	}
	if len(ch.TargetEnc) == 0 {
		return badReq("target is required")
	}
	if in.Secret != nil {
		if *in.Secret == "" {
			ch.SecretEnc = nil
		} else {
			ch.SecretEnc = a.Box.SealString(*in.Secret)
		}
	}
	if in.Events != nil {
		for _, e := range *in.Events {
			if !slices.Contains(alerts.EventTypes, e) {
				return badReq("events must be any of " + strings.Join(alerts.EventTypes, ", "))
			}
		}
		ch.Events = *in.Events
	}
	if ch.Events == nil {
		ch.Events = []string{}
	}
	if in.Enabled != nil {
		ch.Enabled = *in.Enabled
	}
	if ch.Name == "" {
		return badReq("name is required")
	}
	return nil
}

func (a *API) createChannel(c *Ctx) (any, error) {
	o, _, err := a.orgCtx(c, "admin")
	if err != nil {
		return nil, err
	}
	var in channelIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	ch := &store.NotificationChannel{ID: util.NewID("chn"), OrgID: o.ID, Enabled: true, CreatedAt: time.Now()}
	if err := a.applyChannel(ch, &in); err != nil {
		return nil, err
	}
	if err := a.St.Insert(c, a.St.Pool, "notification_channels", ch); err != nil {
		return nil, err
	}
	c.Audit("notification_channel.create", o.ID, "notification_channel", ch.ID, map[string]any{"name": ch.Name, "type": ch.Type, "events": ch.Events})
	return created{a.channelView(*ch)}, nil
}

func (a *API) channelCtx(c *Ctx) (*store.NotificationChannel, error) {
	ch, err := store.GetByID[store.NotificationChannel](c, a.St.Pool, "notification_channels", c.Param("id"))
	if err != nil {
		return nil, err
	}
	return ch, c.Require(auth.Scope{Type: "org", ID: ch.OrgID, OrgID: ch.OrgID}, "admin")
}

func (a *API) updateChannel(c *Ctx) (any, error) {
	ch, err := a.channelCtx(c)
	if err != nil {
		return nil, err
	}
	var in channelIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if in.Type != nil && *in.Type != ch.Type && in.Target == nil {
		return nil, badReq("changing the type needs a new target")
	}
	if err := a.applyChannel(ch, &in); err != nil {
		return nil, err
	}
	if err := a.St.Update(c, a.St.Pool, "notification_channels", ch); err != nil {
		return nil, err
	}
	c.Audit("notification_channel.update", ch.OrgID, "notification_channel", ch.ID, map[string]any{"name": ch.Name, "type": ch.Type, "events": ch.Events, "enabled": ch.Enabled})
	return a.channelView(*ch), nil
}

func (a *API) deleteChannel(c *Ctx) (any, error) {
	ch, err := a.channelCtx(c)
	if err != nil {
		return nil, err
	}
	if err := a.St.Delete(c, a.St.Pool, "notification_channels", ch.ID); err != nil {
		return nil, err
	}
	c.Audit("notification_channel.delete", ch.OrgID, "notification_channel", ch.ID, map[string]string{"name": ch.Name})
	return nil, nil
}

// testChannel sends a test alert so admins can check delivery.
func (a *API) testChannel(c *Ctx) (any, error) {
	ch, err := a.channelCtx(c)
	if err != nil {
		return nil, err
	}
	if a.Alerts == nil {
		return nil, util.Err(503, "api_error", "alerts are not running")
	}
	e := alerts.Event{Type: alerts.Test, OrgID: ch.OrgID, TS: time.Now(), Title: "Theophylax test alert",
		Text: "This channel (" + ch.Name + ") is set up to receive Theophylax alerts.", Link: strings.TrimRight(a.Cfg.PublicURL, "/") + "/settings"}
	if err := a.Alerts.SendTo(c, ch, e); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}, nil
	}
	return map[string]any{"ok": true}, nil
}
