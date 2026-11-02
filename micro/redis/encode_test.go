package redis

import (
	"encoding/json"
	"testing"
)

func TestEncodeEnvelope(t *testing.T) {
	for _, tc := range []struct {
		env  envelope
		data string
		want string
	}{
		{envelope{}, `{"a":1}`, `{"data":{"a":1}}`},
		{envelope{}, ``, `{}`},
		{envelope{ID: "x", Reply: "in", Header: map[string]string{"k": "v"}}, `[1]`, `{"id":"x","reply":"in","header":{"k":"v"},"data":[1]}`},
	} {
		b, err := encodeEnvelope(tc.env, []byte(tc.data))
		if err != nil || string(b) != tc.want {
			t.Errorf("encodeEnvelope = %s, %v; want %s", b, err, tc.want)
		}
		var back envelope
		if err := json.Unmarshal(b, &back); err != nil {
			t.Errorf("%s does not decode: %v", b, err)
		}
	}
	var r reply
	if err := json.Unmarshal(encodeReply(`a"b`, []byte(`{"data":1}`)), &r); err != nil || r.ID != `a"b` || string(r.Data) != `{"data":1}` {
		t.Errorf("reply = %+v, %v", r, err)
	}
}
