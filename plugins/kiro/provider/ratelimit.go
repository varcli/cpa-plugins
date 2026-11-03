package provider

import (
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

type credentialPacingState struct {
	nextSlot time.Time
	parkedTo time.Time
	parkKind string
	parkWhy  string
}

var credentialPacing = struct {
	sync.Mutex
	states map[string]*credentialPacingState
	rng    *rand.Rand
	now    func() time.Time
	sleep  func(time.Duration)
}{
	states: make(map[string]*credentialPacingState),
	rng:    rand.New(rand.NewSource(time.Now().UnixNano())), // #nosec G404
	now:    time.Now,
	sleep:  time.Sleep,
}

const pacingJitter = 0.30

func admitCredential(authID string, cred credential) error {
	if cred.DisableCooling || strings.TrimSpace(authID) == "" {
		return nil
	}
	minInterval, maxInterval, _, err := pacingDurations()
	if err != nil {
		return nil
	}
	credentialPacing.Lock()
	state := credentialPacingStateFor(authID)
	now := credentialPacing.now()
	if now.Before(state.parkedTo) {
		remaining := state.parkedTo.Sub(now)
		kind, why := state.parkKind, state.parkWhy
		credentialPacing.Unlock()
		return parkedCredentialError(kind, why, remaining)
	}
	slot := state.nextSlot
	if slot.Before(now) {
		slot = now
	}
	wait := slot.Sub(now)
	if wait > maxInterval {
		credentialPacing.Unlock()
		return statusError{Code: "rate_limited", Message: fmt.Sprintf("credential is paced for another %v", wait.Round(time.Millisecond)), HTTPStatus: http.StatusTooManyRequests}
	}
	state.nextSlot = slot.Add(jitteredInterval(minInterval, maxInterval))
	credentialPacing.Unlock()
	if wait > 0 {
		credentialPacing.sleep(wait)
	}
	return nil
}

func recordCredentialFailure(authID string, cred credential, err error) {
	if cred.DisableCooling || strings.TrimSpace(authID) == "" {
		return
	}
	typed, ok := err.(statusError)
	if !ok {
		return
	}
	_, _, cooldown, errPacing := pacingDurations()
	if errPacing != nil {
		return
	}
	var until time.Time
	var kind, why string
	switch {
	case typed.HTTPStatus == http.StatusPaymentRequired || typed.Code == "quota_exhausted":
		kind, why = "quota_exhausted", "monthly quota exhausted"
		until = nextUTCMidnight(credentialPacing.now())
	case typed.HTTPStatus == http.StatusForbidden && isSuspendedBody(typed.Body+" "+typed.Message):
		kind, why = "suspended", "account suspended"
		until = credentialPacing.now().Add(cooldown)
	default:
		return
	}
	credentialPacing.Lock()
	state := credentialPacingStateFor(authID)
	state.parkedTo, state.parkKind, state.parkWhy = until, kind, why
	credentialPacing.Unlock()
}

func credentialPacingStateFor(authID string) *credentialPacingState {
	state := credentialPacing.states[authID]
	if state == nil {
		state = &credentialPacingState{}
		credentialPacing.states[authID] = state
	}
	return state
}

func jitteredInterval(minInterval, maxInterval time.Duration) time.Duration {
	gap := minInterval
	if spread := maxInterval - minInterval; spread > 0 {
		gap += time.Duration(credentialPacing.rng.Int63n(int64(spread)))
	}
	gap += time.Duration(float64(gap) * pacingJitter * (credentialPacing.rng.Float64()*2 - 1))
	if gap <= 0 {
		return 0
	}
	if gap > maxInterval {
		return maxInterval
	}
	return gap
}

func pacingDurations() (time.Duration, time.Duration, time.Duration, error) {
	return pacingDurationsFor(loadedConfig())
}

func parkedCredentialError(kind, why string, remaining time.Duration) statusError {
	status := http.StatusTooManyRequests
	code := "rate_limited"
	if kind == "quota_exhausted" {
		status = http.StatusPaymentRequired
		code = "quota_exhausted"
	} else if kind == "suspended" {
		status = http.StatusForbidden
		code = "unauthorized"
	}
	return statusError{
		Code: code, HTTPStatus: status,
		Message: fmt.Sprintf("credential is out of rotation for another %v (%s)", remaining.Round(time.Second), why),
	}
}

func isSuspendedBody(body string) bool {
	body = strings.ToUpper(body)
	return strings.Contains(body, "SUSPENDED") || strings.Contains(body, "USER IS NOT AUTHORIZED TO MAKE THIS CALL")
}

func nextUTCMidnight(now time.Time) time.Time {
	utc := now.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day()+1, 0, 0, 0, 0, time.UTC)
}
