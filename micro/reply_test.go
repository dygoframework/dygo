package micro_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/micro"
)

func TestReplyRoundTrip(t *testing.T) {
	u0 := &User{ID: "1", Name: "<Ada>"}
	reply := micro.EncodeReply(u0, nil)
	data, _ := json.Marshal(u0)
	old, _ := json.Marshal(struct { // the encoding before the envelope was written by hand
		Data json.RawMessage `json:"data,omitempty"`
	}{data})
	if got, want := string(reply), string(old); got != want {
		t.Errorf("reply = %s, want %s", got, want)
	}
	var u *User
	if err := micro.DecodeReply(reply, &u); err != nil || u.Name != "<Ada>" {
		t.Fatalf("u = %+v, %v", u, err)
	}
	if err := micro.DecodeReply(reply, nil); err != nil {
		t.Errorf("nil out: %v", err)
	}
	var n int
	if err := micro.DecodeReply(reply, &n); err == nil || !strings.Contains(err.Error(), "decoding reply data") {
		t.Errorf("type mismatch: %v", err)
	}
	if err := micro.DecodeReply([]byte(`{"data":`), &u); err == nil || !strings.Contains(err.Error(), "decoding reply:") {
		t.Errorf("bad reply: %v", err)
	}
	if err := micro.DecodeReply([]byte(`{}`), &n); err != nil || n != 0 {
		t.Errorf("empty reply: %d, %v", n, err)
	}
	var he *errs.HTTPError
	if err := micro.DecodeReply(micro.EncodeReply(nil, errs.NotFound("gone")), &u); !errors.As(err, &he) || he.Status != 404 {
		t.Errorf("err = %v", err)
	}
	if got := string(micro.EncodeReply(func() {}, nil)); !strings.Contains(got, `"error"`) {
		t.Errorf("unencodable result = %s", got)
	}
}
