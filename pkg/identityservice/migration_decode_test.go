package identityservice

import "testing"

func TestMigrationDecodeTransport(t *testing.T) {
	for _, raw := range []string{`{"group_ratio":{"pro":null}}`, `{"group_ratio":{"pro":1,"pro":0}}`, `{"group_ratio":{"pro":1},"Unknown":true}`, `{"matrix":[{"allowed":null}]}`, `{"matrix":[{"Allowed":true}]}`, `{} {}`, `null`} {
		if _, err := DecodeMigration([]byte(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	in, err := DecodeMigration([]byte(`{"identities":["Friend"],"group_ratio":{"pro":0},"matrix":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := in.GroupRatio["pro"]; !ok || value != 0 {
		t.Fatal("zero lost")
	}
}
