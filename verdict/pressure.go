package verdict

import (
	"shieldtunnel/domain"
)

// integrate computes the trapezoidal fixed-point integral of pressure over the
// time axis. Traces must be time-ordered; the caller guarantees a valid axis.
func integrate(traces []domain.PressureTrace) (domain.Fixed, error) {
	var total domain.Fixed
	for i := 0; i+1 < len(traces); i++ {
		dt := traces[i+1].LogicalTime - traces[i].LogicalTime
		if dt <= 0 {
			return 0, domain.ErrFixedDivideByZero
		}
		sum, err := domain.Add(traces[i].Pressure, traces[i+1].Pressure)
		if err != nil {
			return 0, err
		}
		prod, err := domain.MulInt(sum, dt)
		if err != nil {
			return 0, err
		}
		term, err := domain.DivInt(prod, 2)
		if err != nil {
			return 0, err
		}
		total, err = domain.Add(total, term)
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

// decayRate returns the pressure decay rate (first minus last pressure over the
// elapsed time) as a fixed-point value, with overflow and divide-by-zero checks.
func decayRate(traces []domain.PressureTrace) (domain.Fixed, error) {
	if len(traces) < 2 {
		return 0, nil
	}
	dt := traces[len(traces)-1].LogicalTime - traces[0].LogicalTime
	if dt <= 0 {
		return 0, domain.ErrFixedDivideByZero
	}
	delta, err := domain.Add(traces[0].Pressure, domain.Fixed(-int64(traces[len(traces)-1].Pressure)))
	if err != nil {
		return 0, err
	}
	return domain.DivInt(delta, dt)
}
