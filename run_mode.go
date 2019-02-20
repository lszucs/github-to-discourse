package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/google/go-github/github"
)

const (
	discourseDone = iota
	commentDone
	closeDone
	lockDone
)

const (
	activeTpl = "Hi %s! We are migrating our GitHub issues to Discourse (https://discuss.bitrise.io/c/issues/build-issues). From now on, you can track this issue at: %s"
	staleTpl  = "Hi %s! We are migrating our GitHub issues to Discourse (https://discuss.bitrise.io/c/issues/build-issues). Because this issue has been inactive for more than three months, we will be closing it. If you feel it is still relevant, please open a ticket on Discourse!"
)

var runID string

type runStats struct {
	Processed   int
	Stale       int
	Active      int
	PullRequest int
}

type runMode interface {
	run(issues []*github.Issue, unfinished *github.Issue) (runStats, error)
}

type dryRun struct {
	stats runStats
}

type liveRun struct {
	stats  runStats
	tc     *http.Client
	chkptf *os.File
}

func init() {
	flag.StringVar(&runID, "run-id", "", "--run-id=<string> (created resources will have 'myrunid' baked into title for easier identification)")
}

func (run dryRun) process(i *github.Issue) {
	if i.IsPullRequest() {
		run.stats.PullRequest++
		fmt.Println(fmt.Sprintf("skip %s: is pull request", i.GetHTMLURL()))
		return
	}

	if !isStale(i) {
		run.stats.Active++
		fmt.Println(fmt.Sprintf("%s is active", i.GetHTMLURL()))
	} else {
		run.stats.Stale++
		fmt.Println(fmt.Sprintf("%s is stale", i.GetHTMLURL()))
	}
	run.stats.Processed++
}

func (run dryRun) finish(i *github.Issue) {
	fmt.Println(fmt.Printf("continuing %s", i.GetHTMLURL()))
}

func (run dryRun) run(issues []*github.Issue, unfinished *github.Issue) (runStats, error) {
	for _, i := range issues {
		fmt.Println(fmt.Sprintf("processing issue %s", i.GetHTMLURL()))
		run.process(i)
		// avoid throttling
		time.Sleep(time.Millisecond + 1000)
	}

	return run.stats, nil
}

func (run liveRun) process(i *github.Issue) error {
	if i.IsPullRequest() {
		run.stats.PullRequest++
		fmt.Println(fmt.Sprintf("skip %s: is pull request", i.GetHTMLURL()))
		return nil
	}

	checkpoint := restoredIssue{
		Repo:   i.GetHTMLURL(),
		Owner:  i.GetRepository().GetOwner().GetLogin(),
		IssNum: i.GetNumber(),
	}

	var commentTpl string
	commentTplParams := []interface{}{i.GetUser().GetLogin()}
	if !isStale(i) {

		fmt.Println(fmt.Sprintf("%s is active", i.GetHTMLURL()))
		run.stats.Active++

		// discourse
		title := i.GetTitle()
		if runID != "" {
			title = fmt.Sprintf("[test][%s] %s", runID, i.GetTitle())
		}
		url, err := discourse(i, title, i.GetBody(), discourseCategoryID)
		if err != nil {
			return err
		}

		checkpoint.Done = discourseDone
		if err := saveState(run.chkptf, checkpoint); err != nil {
			return fmt.Errorf("process: %s", err)
		}

		commentTpl = activeTpl
		commentTplParams = append(commentTplParams, url)
	} else {

		run.stats.Stale++

		commentTpl = staleTpl
	}

	if err := comment(i, fmt.Sprintf(commentTpl, commentTplParams...)); err != nil {
		return err
	}

	checkpoint.Done = commentDone
	if err := saveState(run.chkptf, checkpoint); err != nil {
		return fmt.Errorf("process: %s", err)
	}

	// close
	if err := close(i); err != nil {
		return err
	}

	checkpoint.Done = closeDone
	if err := saveState(run.chkptf, checkpoint); err != nil {
		return fmt.Errorf("process: %s", err)
	}

	// lock
	if err := lock(i); err != nil {
		return err
	}

	checkpoint.Done = lockDone
	if err := saveState(run.chkptf, checkpoint); err != nil {
		return fmt.Errorf("process: %s", err)
	}
	run.stats.Processed++

	return nil
}

func (run liveRun) finish(i *github.Issue) error {
	// todo: check if status has changed, e.g.: already closed
	return nil
}

func (run liveRun) run(issues []*github.Issue, unfinished *github.Issue) (runStats, error) {

	if unfinished != nil {
		run.finish(unfinished)
	}

	chkptf, err := os.OpenFile(chkptLog, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	run.chkptf = chkptf

	defer func() {
		if err := chkptf.Close(); err != nil {
			fmt.Printf("warning: closing checkpoint file: %s", err)
			fmt.Println()
		}
	}()

	for _, i := range issues {
		fmt.Println(fmt.Sprintf("processing issue %s", i.GetHTMLURL()))
		if err := run.process(i); err != nil {
			return run.stats, fmt.Errorf("process issue %s: %s", i.GetHTMLURL(), err) // todo: return stats so far or null value object?
		}
		// avoid throttling
		time.Sleep(time.Millisecond + 1000)
	}
	return run.stats, nil
}

func getRunMode(mode string) (runMode, error) {
	switch mode {
	case "dry":
		return dryRun{}, nil
	case "live":
		return liveRun{
			tc: tc,
		}, nil
	default:
		return nil, fmt.Errorf("unkown run mode %s")
	}
}
