package classifier

// maxCached bounds the verdict cache: past it, the oldest answer is dropped.
const maxCached = 10000

// verdictCache keeps the model's answers, first stored first dropped. It is
// not safe for concurrent use: the Classifier's mutex guards it.
type verdictCache struct {
	max   int
	m     map[cacheKey]verdict
	order []cacheKey // stored keys, oldest first
}

func newVerdictCache(max int) *verdictCache {
	return &verdictCache{max: max, m: map[cacheKey]verdict{}}
}

func (c *verdictCache) get(k cacheKey) (verdict, bool) {
	v, ok := c.m[k]
	return v, ok
}

// put stores v under k. Replacing a key keeps its place in the order; a new
// key past the bound drops the oldest one.
func (c *verdictCache) put(k cacheKey, v verdict) {
	if _, ok := c.m[k]; ok {
		c.m[k] = v
		return
	}
	for len(c.order) >= c.max && len(c.order) > 0 {
		delete(c.m, c.order[0])
		c.order = c.order[1:]
	}
	c.m[k] = v
	c.order = append(c.order, k)
}

func (c *verdictCache) len() int { return len(c.m) }
