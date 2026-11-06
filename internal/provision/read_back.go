package provision

import "time"

func (run *execution) verifyUpdate(step Step, after Fields) error {
	for attempt := 0; attempt < 10; attempt++ {
		actual, _, err := run.current(step)
		if err != nil {
			return err
		}
		if sameFields(actual, after) {
			return nil
		}
		// Wait only while the old value remains visible. A third value is a conflict.
		if !sameFields(actual, step.Before) || attempt == 9 {
			return conflict()
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-run.remote.ctx.Done():
			timer.Stop()
			return incomplete()
		case <-timer.C:
		}
	}
	return conflict()
}
