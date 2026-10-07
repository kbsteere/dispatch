package sidebar

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScrollMovesTheContent(t *testing.T) {
	m := NewModel()
	m.viewport.SetHeight(5)
	m.viewport.SetWidth(40)
	m.SetContent(strings.Repeat("line\n", 50))

	m.ScrollDown(3)
	assert.Equal(t, 3, m.YOffset())
	m.ScrollDown(3)
	assert.Equal(t, 6, m.YOffset())
	m.ScrollUp(3)
	assert.Equal(t, 3, m.YOffset())
	m.ScrollUp(10)
	assert.Equal(t, 0, m.YOffset(), "scrolling stops at the top")
}
