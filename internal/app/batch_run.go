package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/batch"
)

// This file is the batch orchestration layer: it ties the store, the engine, and
// the manifest interpreter together, running each job as a background goroutine so
// it survives page refreshes and (via resumeBatchJobs) server restarts. See
// docs/adr/0001-batch-run-engine.md.

// difyRunTimeout bounds a single blocking workflow run. Dify runs can take minutes.
const difyRunTimeout = 10 * time.Minute

const defaultMarketIndexURL = "https://raw.githubusercontent.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/main/plugins/index.json"

// batchMaxConcurrency is the admin-set ceiling a per-job concurrency is clamped to.
func (s *Server) batchMaxConcurrency() int {
	n, err := strconv.Atoi(s.st.GetSetting("batch_max_concurrency", "10"))
	if err != nil || n < 1 {
		return 10
	}
	return n
}

// clampConcurrency keeps an operator's requested concurrency within [1, adminMax].
func (s *Server) clampConcurrency(requested int) int {
	if requested < 1 {
		requested = 1
	}
	if max := s.batchMaxConcurrency(); requested > max {
		requested = max
	}
	return requested
}

// buildProvider constructs the Provider for a job from its target's plugin
// manifest and config. Returns an error if the plugin/target is missing or the
// manifest no longer compiles (e.g. a plugin was deleted after the job started).
func (s *Server) buildProvider(job BatchJob) (batch.Provider, error) {
	tgt, ok := s.st.GetTarget(job.TargetID)
	if !ok {
		return nil, fmt.Errorf("target %d not found", job.TargetID)
	}
	plug, ok := s.st.GetPlugin(tgt.PluginSlug)
	if !ok {
		return nil, fmt.Errorf("plugin %q not found", tgt.PluginSlug)
	}
	m, err := batch.Compile([]byte(plug.Spec))
	if err != nil {
		return nil, fmt.Errorf("plugin %q manifest: %w", tgt.PluginSlug, err)
	}
	cfg := map[string]string{}
	json.Unmarshal([]byte(tgt.Config), &cfg)
	return m.NewProvider(cfg, &http.Client{Timeout: difyRunTimeout}), nil
}

// launchJob starts (or resumes) a job in the background. It is idempotent: a job
// already running in this process is not launched twice.
func (s *Server) launchJob(jobID int64) {
	if _, loaded := s.batchRunning.LoadOrStore(jobID, struct{}{}); loaded {
		return
	}
	job, ok := s.st.GetBatchJob(jobID)
	if !ok {
		s.batchRunning.Delete(jobID)
		return
	}
	prov, err := s.buildProvider(job)
	if err != nil {
		log.Printf("batch job %d: cannot start: %v", jobID, err)
		s.st.FinishJob(jobID, false) // nothing runnable; close it out
		s.batchRunning.Delete(jobID)
		return
	}
	eng := &batch.Engine{Store: s.st, Log: log.Printf}
	spec := batch.JobSpec{JobID: jobID, Concurrency: job.Concurrency, MaxRetries: job.MaxRetries}
	go func() {
		defer s.batchRunning.Delete(jobID)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("batch job %d panicked: %v", jobID, r)
			}
		}()
		if err := eng.RunJob(context.Background(), spec, prov); err != nil {
			log.Printf("batch job %d ended with error: %v", jobID, err)
		}
		if j, ok := s.st.GetBatchJob(jobID); ok {
			s.fireEvent(EventBatchFinished, map[string]any{
				"job_id": j.ID, "status": j.Status, "total": j.Total,
				"succeeded": j.Succeeded, "partial": j.Partial, "failed": j.Failed,
			})
		}
	}()
}

// resumeBatchJobs is called at startup: it requeues items left 'running' by a
// crash and relaunches every job that was mid-flight.
func (s *Server) resumeBatchJobs() {
	if err := s.st.ResetInFlightItems(); err != nil {
		log.Printf("batch resume: reset in-flight items: %v", err)
		return
	}
	for _, id := range s.st.ResumableJobIDs() {
		log.Printf("batch resume: relaunching job %d", id)
		s.launchJob(id)
	}
}
