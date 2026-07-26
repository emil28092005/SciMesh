// Clock: the real implementation of the usecase.Clock port. It lives out here
// because reading the system clock is infrastructure; tests substitute a fixed one.
package infra

import "time"

type System struct{}

func NewClock() System { return System{} }

// Now returns UTC so every timestamp the coordinator writes is comparable
// regardless of the host's timezone.
func (System) Now() time.Time { return time.Now().UTC() }
