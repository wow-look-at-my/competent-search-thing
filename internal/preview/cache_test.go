package preview

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func textPayload(content string) Payload {
	return Payload{Kind: KindText, Text: &TextPreview{Content: content}}
}

func TestCacheGetPutAndReplace(t *testing.T) {
	c := newPayloadCache()
	_, ok := c.get("missing")
	require.False(t, ok)

	c.put("a", textPayload("first"))
	p, ok := c.get("a")
	require.True(t, ok)
	require.Equal(t, "first", p.Text.Content)

	// Replacing under the same key keeps one entry and re-accounts the
	// bytes.
	c.put("a", textPayload("second value that is longer"))
	p, ok = c.get("a")
	require.True(t, ok)
	require.Equal(t, "second value that is longer", p.Text.Content)
	require.Equal(t, 1, c.len())
	require.Equal(t, cacheEntryOverhead+len("second value that is longer"), c.byteSize())
}

func TestCacheEntryCountBound(t *testing.T) {
	c := newPayloadCache()
	for i := 0; i < cacheMaxEntries+10; i++ {
		c.put(fmt.Sprintf("k%03d", i), textPayload("x"))
	}
	require.Equal(t, cacheMaxEntries, c.len())
	// The oldest entries were evicted, the newest survive.
	_, ok := c.get("k000")
	require.False(t, ok)
	_, ok = c.get(fmt.Sprintf("k%03d", cacheMaxEntries+9))
	require.True(t, ok)
}

func TestCacheByteBudgetBound(t *testing.T) {
	c := newPayloadCache()
	big := strings.Repeat("x", 1<<20) // 1 MiB per entry
	for i := 0; i < 20; i++ {
		c.put(fmt.Sprintf("k%02d", i), textPayload(big))
	}
	require.LessOrEqual(t, c.byteSize(), cacheBudgetBytes)
	require.Less(t, c.len(), 20, "the byte budget must have evicted entries")
}

func TestCacheLRUOrder(t *testing.T) {
	c := newPayloadCache()
	big := strings.Repeat("x", 6<<20) // 6 MiB: three fit, a fourth evicts
	c.put("a", textPayload(big))
	c.put("b", textPayload(big))
	// Touch "a" so "b" is now the least recently used.
	_, ok := c.get("a")
	require.True(t, ok)
	c.put("c", textPayload(big)) // 18 MiB > budget: evicts "b"
	_, ok = c.get("b")
	require.False(t, ok, "the least recently used entry is evicted first")
	_, ok = c.get("a")
	require.True(t, ok, "the recently touched entry survives")
	_, ok = c.get("c")
	require.True(t, ok)
}

func TestCacheOversizePayloadNotStored(t *testing.T) {
	c := newPayloadCache()
	c.put("huge", textPayload(strings.Repeat("x", cacheBudgetBytes)))
	require.Equal(t, 0, c.len())
	_, ok := c.get("huge")
	require.False(t, ok)
}

func TestPayloadSizeCountsImageURI(t *testing.T) {
	p := Payload{Kind: KindImage, Image: &ImagePreview{DataURI: strings.Repeat("d", 100)}}
	require.Equal(t, cacheEntryOverhead+100, payloadSize(p))
	require.Equal(t, cacheEntryOverhead, payloadSize(Payload{Kind: KindMeta}))
}
