package probe

import (
	"strconv"
	"time"
)

const defaultTimeout = 15 * time.Second

func itoa(i int) string { return strconv.Itoa(i) }
