package filter

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestChunk(t *testing.T) {
	assert.Nil(t, Chunk([]int{}, 2))
	assert.Equal(t, [][]int{{1, 2}, {3, 4}, {5}}, Chunk([]int{1, 2, 3, 4, 5}, 2))
	assert.Equal(t, [][]int{{1, 2, 3}}, Chunk([]int{1, 2, 3}, 0))

	// Appending to a chunk must not overwrite the next chunk.
	chunks := Chunk([]int{1, 2, 3, 4}, 2)
	_ = append(chunks[0], 99)
	assert.Equal(t, []int{3, 4}, chunks[1])
}

func TestKeys(t *testing.T) {
	m := map[string]int{"b": 2, "a": 1, "c": 3}
	assert.ElementsMatch(t, []string{"a", "b", "c"}, Keys(m))
	assert.Equal(t, []string{"a", "b", "c"}, SortedKeys(m))
	assert.Empty(t, Keys(map[string]int(nil)))
}
