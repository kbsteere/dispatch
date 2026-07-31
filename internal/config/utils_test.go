package config

import (
	"testing"

	"github.com/kbsteere/dispatch/v4/internal/utils"
)

func TestMergeColumnConfigs(t *testing.T) {
	t.Run("section width overrides default, keeps default hidden", func(t *testing.T) {
		def := ColumnConfig{Width: utils.IntPtr(10), Hidden: utils.BoolPtr(true)}
		section := ColumnConfig{Width: utils.IntPtr(20)}

		got := MergeColumnConfigs(def, section)

		if got.Width == nil || *got.Width != 20 {
			t.Errorf("Width: got = %v, want = 20", got.Width)
		}
		if got.Hidden == nil || *got.Hidden != true {
			t.Errorf("Hidden: got = %v, want = true", got.Hidden)
		}
	})

	t.Run("section hidden overrides default, keeps default width", func(t *testing.T) {
		def := ColumnConfig{Width: utils.IntPtr(10), Hidden: utils.BoolPtr(true)}
		section := ColumnConfig{Hidden: utils.BoolPtr(false)}

		got := MergeColumnConfigs(def, section)

		if got.Width == nil || *got.Width != 10 {
			t.Errorf("Width: got = %v, want = 10", got.Width)
		}
		if got.Hidden == nil || *got.Hidden != false {
			t.Errorf("Hidden: got = %v, want = false", got.Hidden)
		}
	})

	t.Run("empty section keeps default", func(t *testing.T) {
		def := ColumnConfig{Width: utils.IntPtr(10), Hidden: utils.BoolPtr(true)}

		got := MergeColumnConfigs(def, ColumnConfig{})

		if got.Width == nil || *got.Width != 10 {
			t.Errorf("Width: got = %v, want = 10", got.Width)
		}
		if got.Hidden == nil || *got.Hidden != true {
			t.Errorf("Hidden: got = %v, want = true", got.Hidden)
		}
	})
}
