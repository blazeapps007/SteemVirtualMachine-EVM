package nodepool

import (
	"reflect"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	got := Parse(" https://a/ , https://b", "https://a,,https://c ")
	want := []string{"https://a", "https://b", "https://c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse = %v, want %v", got, want)
	}
}

func TestRoundRobinSpreadsCalls(t *testing.T) {
	p := New(RoundRobin, "a,b,c")
	var firsts []string
	for i := 0; i < 6; i++ {
		firsts = append(firsts, p.Order()[0])
	}
	if want := []string{"a", "b", "c", "a", "b", "c"}; !reflect.DeepEqual(firsts, want) {
		t.Fatalf("first nodes %v, want %v", firsts, want)
	}
}

func TestStickyPrefersFirstHealthy(t *testing.T) {
	now := time.Unix(1000, 0)
	p := New(Sticky, "own,b,c")
	p.now = func() time.Time { return now }
	if o := p.Order(); o[0] != "own" || p.Order()[0] != "own" {
		t.Fatalf("sticky order %v", o)
	}
	p.Fail("own")
	if o := p.Order(); !reflect.DeepEqual(o, []string{"b", "c", "own"}) {
		t.Fatalf("benched node not last: %v", o)
	}
	now = now.Add(31 * time.Second)
	if o := p.Order(); o[0] != "own" {
		t.Fatalf("node not back after its cooldown: %v", o)
	}
}

func TestCooldownGrowsAndResets(t *testing.T) {
	now := time.Unix(1000, 0)
	p := New(RoundRobin, "a,b")
	p.now = func() time.Time { return now }
	var got []time.Duration
	for i := 0; i < 7; i++ {
		got = append(got, p.Fail("a"))
	}
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 10 * time.Minute, 10 * time.Minute}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cooldowns %v, want %v", got, want)
	}
	for i := 0; i < 4; i++ {
		if o := p.Order(); o[0] != "b" {
			t.Fatalf("benched node served first: %v", o)
		}
	}
	p.OK("a")
	if d := p.Fail("a"); d != 30*time.Second {
		t.Fatalf("streak not reset by OK: %v", d)
	}
}

func TestAllBenchedStillTriesSoonestFirst(t *testing.T) {
	now := time.Unix(1000, 0)
	p := New(RoundRobin, "a,b")
	p.now = func() time.Time { return now }
	p.Fail("a")
	p.Fail("a") // a: 1m
	p.Fail("b") // b: 30s
	if o := p.Order(); !reflect.DeepEqual(o, []string{"b", "a"}) {
		t.Fatalf("order %v, want [b a]", o)
	}
}
