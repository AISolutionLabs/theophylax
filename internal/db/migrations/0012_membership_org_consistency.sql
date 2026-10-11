-- A membership only confers a role inside its own organization. Remove rows
-- whose org_id disagrees with the scope they name. Such rows could be created
-- by an identity provider's role mapping that pointed at another
-- organization's scope, before mappings were validated against the IdP's org.
DELETE FROM memberships m WHERE m.scope_type = 'org' AND m.scope_id <> m.org_id;
DELETE FROM memberships m USING teams t
  WHERE m.scope_type = 'team' AND t.id = m.scope_id AND t.org_id <> m.org_id;
DELETE FROM memberships m USING projects p
  WHERE m.scope_type = 'project' AND p.id = m.scope_id AND p.org_id <> m.org_id;
