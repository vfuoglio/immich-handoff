package notify

import "testing"

// the file the documentation ships must be one the app accepts
func TestTheExampleFileIsValid(t *testing.T) {
	d, err := FromEnv(env(map[string]string{"NOTIFY_FILE": "../../notify.example.yaml", "HA_WEBHOOK_ID": "abc", "SMTP_PASSWORD": "pw"}))
	if err != nil || len(d) != 3 {
		t.Fatalf("%d destinations: %v", len(d), err)
	}
	if d[2].Format != FormatEmail || d[2].Mail == nil || d[2].Mail.Host != "smtp.example.com" {
		t.Errorf("%+v", d[2])
	}
}
