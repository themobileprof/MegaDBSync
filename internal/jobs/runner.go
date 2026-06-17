package jobs

import (
	"context"
	"sync"
	"time"

	"github.com/mdas/mdas/internal/migrate"
	"github.com/mdas/mdas/internal/store"
)

type Runner struct {
	Store    *store.Store
	Engine   *migrate.Engine
	mu       sync.Mutex
	cancel   context.CancelFunc
	notify   chan struct{}
}

func NewRunner(st *store.Store) *Runner {
	r := &Runner{
		Store:  st,
		notify: make(chan struct{}, 1),
	}
	r.Engine = &migrate.Engine{
		Store: st,
		OnProgress: func() {
			select {
			case r.notify <- struct{}{}:
			default:
			}
		},
	}
	return r
}

func (r *Runner) Notify() <-chan struct{} {
	return r.notify
}

func (r *Runner) StartJob(jobID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		return errJobRunning
	}
	job, err := r.Store.GetJob(jobID)
	if err != nil {
		return err
	}
	if job.Status != store.JobPending && job.Status != store.JobPaused {
		return errInvalidStatus
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go r.run(ctx, job)
	return nil
}

func (r *Runner) CancelJob() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
}

func (r *Runner) run(ctx context.Context, job store.Job) {
	defer func() {
		r.mu.Lock()
		r.cancel = nil
		r.mu.Unlock()
		select {
		case r.notify <- struct{}{}:
		default:
		}
	}()

	src, err := r.Store.GetConnection(job.SourceID)
	if err != nil {
		r.failJob(job, err.Error())
		return
	}
	dst, err := r.Store.GetConnection(job.DestID)
	if err != nil {
		r.failJob(job, err.Error())
		return
	}
	srcPass, err := r.Store.ConnectionPassword(job.SourceID)
	if err != nil {
		r.failJob(job, err.Error())
		return
	}
	dstPass, err := r.Store.ConnectionPassword(job.DestID)
	if err != nil {
		r.failJob(job, err.Error())
		return
	}

	var runErr error
	switch job.Type {
	case store.JobBulkFull:
		runErr = r.Engine.RunBulk(ctx, job, src, dst, srcPass, dstPass)
	case store.JobIncrementalSync:
		runErr = r.Engine.RunIncremental(ctx, job, src, dst, srcPass, dstPass)
	default:
		runErr = errUnknownJobType
	}

	if runErr != nil {
		if ctx.Err() != nil {
			job.Status = store.JobCancelled
			job.ErrorMessage = "cancelled"
		} else {
			job.Status = store.JobFailed
			job.ErrorMessage = runErr.Error()
		}
		now := time.Now().UTC()
		job.CompletedAt = &now
		_ = r.Store.UpdateJob(job)
		_ = r.Store.LogEvent(job.ID, "error", runErr.Error())
	}
}

func (r *Runner) failJob(job store.Job, msg string) {
	job.Status = store.JobFailed
	job.ErrorMessage = msg
	now := time.Now().UTC()
	job.CompletedAt = &now
	_ = r.Store.UpdateJob(job)
	_ = r.Store.LogEvent(job.ID, "error", msg)
}

type Scheduler struct {
	Store  *store.Store
	Runner *Runner
	stop   chan struct{}
}

func NewScheduler(st *store.Store, runner *Runner) *Scheduler {
	return &Scheduler{Store: st, Runner: runner, stop: make(chan struct{})}
}

func (s *Scheduler) Start() {
	go s.loop()
}

func (s *Scheduler) Stop() {
	close(s.stop)
}

func (s *Scheduler) loop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.maybeRunIncremental()
		}
	}
}

func (s *Scheduler) maybeRunIncremental() {
	st, err := s.Store.GetSettings()
	if err != nil || st.ScheduleCron == "" {
		return
	}
	if !cronDue(st.ScheduleCron, time.Now()) {
		return
	}
	if st.ScheduleSourceID == "" || st.ScheduleDestID == "" {
		return
	}
	active, _ := s.Store.ActiveJob()
	if active != nil {
		return
	}
	job := store.Job{
		Type:           store.JobIncrementalSync,
		SourceID:       st.ScheduleSourceID,
		DestID:         st.ScheduleDestID,
		BatchSize:      st.DefaultBatchSize,
		ParallelTables: st.DefaultParallel,
	}
	job, _ = s.Store.CreateJob(job)
	_ = s.Store.LogEvent(job.ID, "info", "Scheduled incremental sync started")
	_ = s.Runner.StartJob(job.ID)
}

// cronDue supports simple patterns: "@hourly", "@daily", or "0 */N * * *" minute field with */N.
func cronDue(expr string, now time.Time) bool {
	switch expr {
	case "@hourly":
		return now.Minute() == 0
	case "@daily":
		return now.Hour() == 2 && now.Minute() == 0
	case "0 */4 * * *":
		return now.Minute() == 0 && now.Hour()%4 == 0
	case "0 */6 * * *":
		return now.Minute() == 0 && now.Hour()%6 == 0
	case "0 */12 * * *":
		return now.Minute() == 0 && now.Hour()%12 == 0
	default:
		return now.Minute() == 0 && now.Hour()%4 == 0
	}
}

var (
	errJobRunning     = errString("a job is already running")
	errInvalidStatus  = errString("job cannot be started in its current status")
	errUnknownJobType = errString("unknown job type")
)

type errString string

func (e errString) Error() string { return string(e) }
