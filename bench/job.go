package bench

import (
	"bytes"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"code.cloudfoundry.org/commandrunner"
)

type Job struct {
	Runner commandrunner.CommandRunner

	// Path to grootfs binary
	GrootFSBinPath string

	// The GrootFS store path, where blobs/bundles/cache are stored
	StorePath string

	// Does what it says on the tin
	LogLevel string

	// The image to be downloaded
	Image string

	// Run benchmark using quotas
	UseQuota bool

	// The number of concurrent workers to run
	Concurrency int

	// The total number of bundles to be created
	TotalBundles int

	// Hold the results for each iteration
	results chan *Result
}

// Run is a blocking operation. It blocks until all concurrent cmd execution is
// completed.
func (j *Job) Run() Summary {
	if j.Concurrency == 0 {
		j.Concurrency = runtime.NumCPU()
	}

	j.results = make(chan *Result, j.TotalBundles)

	start := time.Now()
	j.runWorkers()
	totalDuration := time.Since(start)

	close(j.results)
	return SummarizeResults(
		SumSpec{
			Duration:    totalDuration,
			Concurrency: j.Concurrency,
			UseQuota:    j.UseQuota,
			ResChan:     j.results,
		})
}

func (j *Job) runWorkers() {
	var wg sync.WaitGroup
	wg.Add(j.TotalBundles)

	for i := 0; i < j.Concurrency; i++ {
		go func(conc int) {
			for i := 0; i < j.TotalBundles/j.Concurrency; i++ {
				defer wg.Done()
				j.run(conc)
			}
		}(i)
	}

	// Handle some leftovers (i.e: 11 % 3)
	for i := 0; i < j.TotalBundles%j.Concurrency; i++ {
		go func(i int) {
			defer wg.Done()
			j.run(i)
		}(i)
	}

	wg.Wait()
}

func (j *Job) run(i int) {
	start := time.Now()
	cmd := j.grootfsCmd(i)

	buffer := bytes.NewBuffer([]byte{})
	cmd.Stdout = buffer
	cmd.Stderr = buffer

	var cmdErr error
	if err := j.Runner.Run(cmd); err != nil {
		cmdErr = fmt.Errorf("%s, %s", err, buffer.String())
	}
	j.results <- &Result{
		Err:      cmdErr,
		Duration: time.Since(start),
	}
}

func (j *Job) grootfsCmd(workerId int) *exec.Cmd {
	args := []string{
		"--store",
		j.StorePath,
		"--log-level",
		j.LogLevel,
		"create",
	}

	if j.UseQuota {
		args = append(args, "--disk-limit-size-bytes", "1019430400")
	}

	args = append(args,
		j.Image,
		fmt.Sprintf("image-%d-%d", workerId, time.Now().UnixNano()),
	)

	return exec.Command(j.GrootFSBinPath, args...)
}

type Result struct {
	// Original error from grootfs if it occurrs
	Err error

	// Duration took by grootfs bin to run
	Duration time.Duration
}

type SumSpec struct {
	Duration    time.Duration
	Concurrency int
	UseQuota    bool
	ResChan     chan *Result
}

// Summary represents some metrics while running grootfs with given input
type Summary struct {
	TotalDuration        time.Duration `json:"total_duration"`
	BundlesPerSecond     float64       `json:"bundles_per_second"`
	RanWithQuota         bool          `json:"ran_with_quota"`
	AverageTimePerBundle float64       `json:"average_time_per_bundle"`
	TotalErrorsAmt       int           `json:"total_errors_amt"`
	ErrorRate            float64       `json:"error_rate"`
	TotalBundles         int           `json:"total_bundles"`
	ConcurrencyFactor    int           `json:"concurrency_factor"`
	ErrorMessages        []string      `json:"-"`
}

func SummarizeResults(spec SumSpec) Summary {
	summary := Summary{
		ConcurrencyFactor: spec.Concurrency,
		TotalDuration:     spec.Duration,
		RanWithQuota:      spec.UseQuota,
	}

	errors := []string{}

	averageTimePerBundle := 0.0
	for res := range spec.ResChan {
		summary.TotalBundles++

		if res.Err != nil {
			summary.TotalErrorsAmt++
			errors = append(errors, fmt.Sprintf("could not create bundle %d: %s\n", summary.TotalBundles, res.Err))
		} else {
			averageTimePerBundle += res.Duration.Seconds()
		}
	}

	createdBundles := float64(summary.TotalBundles - summary.TotalErrorsAmt)
	summary.BundlesPerSecond = createdBundles / spec.Duration.Seconds()
	summary.ErrorRate = float64(summary.TotalErrorsAmt*100) / float64(summary.TotalBundles)
	if createdBundles == float64(0) {
		summary.AverageTimePerBundle = float64(-1)
	} else {
		summary.AverageTimePerBundle = averageTimePerBundle / createdBundles
	}
	summary.TotalDuration = spec.Duration
	summary.ErrorMessages = errors
	return summary
}
