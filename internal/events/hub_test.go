package events

import "testing"

func TestTopicMatch(t *testing.T) {
	cases := []struct {
		pattern, topic string
		want           bool
	}{
		{"deploy.*.stdout", "deploy.foodmanager.stdout", true},
		{"deploy.*.stdout", "deploy.foo.stderr", false},
		{"deploy.*.stdout", "deploy.a.b.stdout", false}, // too many segments
		{"deploy.*.stdout", "deploy.stdout", false},      // too few segments
		{"deploy.*.state", "deploy.foo.state", true},
		{"*", "foo", true},
		{"*", "foo.bar", false},
		{"cfg.change", "cfg.change", true},
		{"cfg.change", "cfg.other", false},
		{"deploy.*.*", "deploy.foo.stdout", true},
	}
	for _, c := range cases {
		if got := topicMatch(c.pattern, c.topic); got != c.want {
			t.Errorf("topicMatch(%q, %q) = %v, want %v", c.pattern, c.topic, got, c.want)
		}
	}
}

func TestWildcardDelivery(t *testing.T) {
	h := New()
	sub, cancel := h.Subscribe([]string{"deploy.*.stdout"}, 0)
	defer cancel()

	h.Publish("deploy.foodmanager.stdout", "stdout", []byte(`{"line":"building"}`))
	h.Publish("deploy.other.state", "state", []byte(`{}`)) // must NOT match

	select {
	case e := <-sub.Ch():
		if e.Topic != "deploy.foodmanager.stdout" {
			t.Fatalf("got topic %q", e.Topic)
		}
	default:
		t.Fatal("wildcard subscriber received no event")
	}

	select {
	case e := <-sub.Ch():
		t.Fatalf("unexpected extra event on topic %q", e.Topic)
	default:
	}
}

func TestWildcardReplay(t *testing.T) {
	h := New()
	// Publish before subscribing — exercises the ring replay path.
	h.Publish("deploy.foo.stdout", "stdout", []byte(`{"line":"1"}`))
	h.Publish("deploy.bar.stdout", "stdout", []byte(`{"line":"2"}`))

	sub, cancel := h.Subscribe([]string{"deploy.*.stdout"}, 0)
	defer cancel()

	var got []uint64
	for i := 0; i < 2; i++ {
		select {
		case e := <-sub.Ch():
			got = append(got, e.ID)
		default:
			t.Fatalf("missing replayed event %d", i)
		}
	}
	if len(got) != 2 || got[0] > got[1] {
		t.Fatalf("replay not in ascending ID order: %v", got)
	}
}
