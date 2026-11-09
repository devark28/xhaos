package classifier

import "testing"

func TestVerdictCacheDropsTheOldestPastTheBound(t *testing.T) {
	c := newVerdictCache(3)
	key := func(i int) cacheKey { return cacheKey{"/p", string(rune('a' + i))} }
	for i := 0; i < 5; i++ {
		c.put(key(i), verdict{allow: true, reason: "r"})
	}
	if c.len() != 3 {
		t.Fatalf("len = %d, want 3", c.len())
	}
	for i := 0; i < 2; i++ {
		if _, ok := c.get(key(i)); ok {
			t.Errorf("entry %d should have been dropped", i)
		}
	}
	for i := 2; i < 5; i++ {
		if _, ok := c.get(key(i)); !ok {
			t.Errorf("entry %d should still be cached", i)
		}
	}
}

func TestVerdictCacheReplaceKeepsTheSizeAndOrder(t *testing.T) {
	c := newVerdictCache(2)
	a, b, d := cacheKey{"/p", "a"}, cacheKey{"/p", "b"}, cacheKey{"/p", "d"}
	c.put(a, verdict{allow: true})
	c.put(b, verdict{allow: true})
	c.put(a, verdict{allow: false, reason: "changed"}) // replacing: no eviction
	if c.len() != 2 {
		t.Fatalf("len = %d after replacing, want 2", c.len())
	}
	if v, ok := c.get(a); !ok || v.allow || v.reason != "changed" {
		t.Fatalf("a = %+v, %v; want the replaced verdict", v, ok)
	}
	c.put(d, verdict{allow: true}) // a is still the oldest: it goes
	if _, ok := c.get(a); ok {
		t.Error("a should be dropped first")
	}
	if _, ok := c.get(b); !ok {
		t.Error("b should be kept")
	}
}

func TestDecideCacheIsBounded(t *testing.T) {
	c := New(nil, Config{})
	for i := 0; i < maxCached+10; i++ {
		c.cache.put(cacheKey{"/p", string(rune(i))}, verdict{allow: true})
	}
	if c.cache.len() != maxCached {
		t.Fatalf("cache holds %d entries, want %d", c.cache.len(), maxCached)
	}
}
