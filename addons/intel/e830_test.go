package intel

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_E830(t *testing.T) {
	p, d := E830("e830")
	assert.NotNil(t, p)
	assert.NotNil(t, d)

	p, d = E830("not_e830")
	assert.Nil(t, p)
	assert.Nil(t, d)
}
