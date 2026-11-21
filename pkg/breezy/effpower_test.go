// SPDX-License-Identifier: GPL-3.0-or-later

package breezy

import (
	"testing"

	"github.com/matryer/is"
)

func TestEffectivePower(t *testing.T) {
	is := is.New(t)
	on, ok := EffectivePower(map[ParamID][]byte{0x0001: {0}, 0x0007: {1}})
	is.True(ok && on) // timer active counts as on
	on, ok = EffectivePower(map[ParamID][]byte{0x0001: {0}, 0x0007: {0}})
	is.True(ok && !on)
	on, ok = EffectivePower(map[ParamID][]byte{0x0001: {1}})
	is.True(ok && on)
	_, ok = EffectivePower(map[ParamID][]byte{})
	is.True(!ok)
}
