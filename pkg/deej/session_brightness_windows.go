package deej

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"
)

const (
	// twinkle tray's own cli. the microsoft store build registers this as an
	// AppExecutionAlias, so it resolves on PATH without knowing the package path
	brightnessHelperExecutable = "Twinkle-Tray.exe"

	// how many monitors we expose. these are created up front rather than enumerated:
	// asking the helper for its monitor list spawns a process and takes about a second,
	// which is far too slow for the slider path, and at login deej would race the helper's
	// own detection and see fewer monitors than are attached. sessions beyond the monitors
	// actually present simply never receive events
	brightnessMonitorCount = 4

	// one invocation of the helper measured ~170ms, and DDC/CI is slow in its own right,
	// so writes are rate limited. the worker always applies the newest value, meaning a
	// fast sweep drops intermediate positions instead of queueing them
	minTimeBetweenBrightnessWrites = 250 * time.Millisecond

	brightnessSessionKeyFormat = "monitor %d (brightness)"
)

// brightnessSession drives one monitor's backlight through twinkle tray's cli.
// it satisfies Session, so deej treats it exactly like an audio session
type brightnessSession struct {
	baseSession

	// twinkle tray's monitor index as --MonitorNum wants it, which is 1-based. note that
	// --List reports the same indices 0-based, so its output cannot be passed through
	// unadjusted
	monitorNum int

	lock sync.Mutex

	// the value deej believes this session holds. handleSliderMoveEvent calls GetVolume
	// on every event and compares it for exact equality against the incoming value, so
	// this has to be the quantized value we were last handed. reading the real brightness
	// back would never compare equal, and every event would spawn a process
	desired float32

	// owned solely by the worker goroutine
	applied float32
	warned  bool
	checked bool

	done chan struct{}
}

func newBrightnessSession(logger *zap.SugaredLogger, monitorNum int) *brightnessSession {
	key := fmt.Sprintf(brightnessSessionKeyFormat, monitorNum)

	s := &brightnessSession{
		monitorNum: monitorNum,
		desired:    -1,
		applied:    -1,
		done:       make(chan struct{}),
	}

	s.logger = logger.Named(key)
	s.master = true
	s.name = key
	s.humanReadableDesc = key

	go s.run()

	s.logger.Debugw(sessionCreationLogMessage, "session", s)

	return s
}

func (s *brightnessSession) GetVolume() float32 {
	s.lock.Lock()
	defer s.lock.Unlock()

	return s.desired
}

func (s *brightnessSession) SetVolume(v float32) error {
	s.lock.Lock()
	s.desired = v
	s.lock.Unlock()

	// deliberately does no work here. the slider path is synchronous from the serial
	// reader through to this call over unbuffered channels, so blocking would stall every
	// other slider too, and would leave Stop() hanging on its own unbuffered send
	return nil
}

func (s *brightnessSession) run() {
	ticker := time.NewTicker(minTimeBetweenBrightnessWrites)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return

		case <-ticker.C:
			s.lock.Lock()
			desired := s.desired
			s.lock.Unlock()

			if desired < 0 || desired == s.applied {
				continue
			}

			// an out-of-range monitor number is accepted and then silently ignored, so a
			// write landing nowhere looks identical to one that worked. say so once
			if !s.checked {
				s.checked = true
				s.warnIfMonitorMissing()
			}

			if err := s.apply(desired); err != nil {

				// warn once per outage rather than several times a second. the helper has to
				// already be running for its cli to do anything, so quitting it lands here
				if !s.warned {
					s.logger.Warnw("Failed to set monitor brightness, is Twinkle Tray running?",
						"error", err)

					s.warned = true
				}

				continue
			}

			s.applied = desired
			s.warned = false
		}
	}
}

func (s *brightnessSession) apply(v float32) error {
	percent := int(v*100 + 0.5)

	if percent < 0 {
		percent = 0
	} else if percent > 100 {
		percent = 100
	}

	cmd := exec.Command(brightnessHelperExecutable,
		fmt.Sprintf("--MonitorNum=%d", s.monitorNum),
		fmt.Sprintf("--Set=%d", percent))

	// the helper is a gui app; without this a console window flashes on every write
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run brightness helper: %w", err)
	}

	s.logger.Debugw("Adjusting monitor brightness", "to", percent)

	return nil
}

// warnIfMonitorMissing checks this session against the monitors the helper can actually
// see. purely diagnostic, and only ever called from the worker goroutine
func (s *brightnessSession) warnIfMonitorMissing() {
	available, err := listMonitorNumbers()
	if err != nil {
		s.logger.Debugw("Failed to list monitors for verification", "error", err)
		return
	}

	if !available[s.monitorNum] {
		s.logger.Warnw("Monitor not reported by Twinkle Tray, writes will be silently ignored",
			"monitorNum", s.monitorNum,
			"available", available)
	}
}

// listMonitorNumbers returns the monitor numbers the helper can see, as --MonitorNum
// wants them. the exit code is deliberately ignored: --List exits non-zero even when it
// succeeds, so the output is the only trustworthy signal
func listMonitorNumbers() (map[int]bool, error) {
	cmd := exec.Command(brightnessHelperExecutable, "--List")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	out, err := cmd.Output()
	if len(out) == 0 {
		if err != nil {
			return nil, fmt.Errorf("run brightness helper: %w", err)
		}

		return nil, errors.New("brightness helper reported no monitors")
	}

	available := map[int]bool{}

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)

		if !strings.HasPrefix(line, "MonitorNum:") {
			continue
		}

		num, convErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "MonitorNum:")))
		if convErr != nil {
			continue
		}

		// --List counts from zero, --MonitorNum counts from one
		available[num+1] = true
	}

	return available, nil
}

// Release is a no-op. brightness sessions are cached on the session finder and handed
// out again on every refresh, so tearing down the worker here would kill it while later
// refreshes still depend on it. the finder stops them when deej itself shuts down
func (s *brightnessSession) Release() {}

func (s *brightnessSession) stop() {
	close(s.done)
}

func (s *brightnessSession) String() string {
	return fmt.Sprintf(sessionStringFormat, s.humanReadableDesc, s.GetVolume())
}
