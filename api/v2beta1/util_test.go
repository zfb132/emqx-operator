/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v2beta1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCloneAndMergeMap(t *testing.T) {
	t.Run("merge maps", func(t *testing.T) {
		dst := map[string]string{
			"a": "1",
			"b": "2",
		}
		src := map[string]string{
			"b": "3",
			"c": "4",
		}
		last := map[string]string{
			"b": "42",
			"d": "5",
		}
		assert.Equal(t, map[string]string{
			"a": "1",
			"b": "2",
			"c": "4",
			"d": "5",
		}, CloneAndMergeMap(dst, src, last))
	})
}
