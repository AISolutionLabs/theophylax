package guardrails

import "testing"

// redactOne runs the secret detectors over s and reports whether anything was
// redacted and the resulting text.
func redactOne(s string) (bool, string) {
	e := New([]byte("k"), nil)
	out, changed := e.redactSecrets(s, DefaultSecretEntities, true, map[string]int{}, map[string]string{})
	return changed, out
}

// TestSecretAssignmentNames covers credentials assigned to compound key names
// (DB_PASSWORD, jwt.secret, X-Api-Key). A bare \b never matches between '_' and
// the keyword, which previously let underscore-joined names leak to the model.
func TestSecretAssignmentNames(t *testing.T) {
	secret := []string{
		"DB_PASSWORD=Xk9mPq2vL8nR4tWz",
		"REDIS_PASSWORD=hunter2hunter2",
		"JWT_SIGNING_SECRET=aB3dF6hJ9kL2mN5pQ8rS",
		"MYSQL_ROOT_PASSWORD=Xk9mPq2vL8nR4tWz",
		"X_SECRET=Xk9mPq2vL8nR4tWzA7bC",
		"app.api_key: a1b2c3d4e5f6g7h8",
		"CLIENT_SECRET=s3cr3tVal9eX",
		"password=letmein2024",
	}
	for _, in := range secret {
		if changed, out := redactOne(in); !changed {
			t.Errorf("expected redaction for %q, got %q", in, out)
		}
	}
}

// TestSecretAssignmentFalsePositives keeps ordinary source and placeholders
// untouched so a coding agent's prompt is not degraded.
func TestSecretAssignmentFalsePositives(t *testing.T) {
	benign := []string{
		"password = userPassword",
		"api_key: process.env.API_KEY",
		"secret = config.get()",
		"password := user_input",
		"client_secret = os.getenv(\"CLIENT_SECRET\")",
		"password: ${DB_PASS}",
		"password = None",
		"api_key = <your-key-here>",
		"password = changeme",
		"secret: self.password",
	}
	for _, in := range benign {
		if changed, out := redactOne(in); changed {
			t.Errorf("false positive on benign source %q -> %q", in, out)
		}
	}
}

// TestSecretValueDetectorsByValue confirms format-distinctive secrets are still
// caught regardless of the variable name around them.
func TestSecretValueDetectorsByValue(t *testing.T) {
	byValue := []string{
		"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"GITHUB_TOKEN=ghp_1234567890abcdefghijklmnopqrstuvwxyz12",
		"DATABASE_URL=postgres://app:Tr0ub4dorPass@db.internal:5432/main",
	}
	for _, in := range byValue {
		if changed, _ := redactOne(in); !changed {
			t.Errorf("expected value-based redaction for %q", in)
		}
	}
}
