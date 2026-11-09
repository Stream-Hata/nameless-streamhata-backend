package feedback

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type issueSnapshot struct {
	title     string
	state     string
	labels    string
	assignees string
	milestone string
	comments  int
}

func (s *Service) IssueWatcher(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.watchIssues(ctx)
		}
	}
}

func (s *Service) watchIssues(ctx context.Context) {
	if s.db == nil {
		return
	}
	rows, err := s.db.QueryContext(ctx, `SELECT prefix, number, repo, issue_number
		FROM feedback_reports WHERE issue_number > 0 AND repo <> '' ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		return
	}
	type trackedIssue struct {
		prefix string
		number int
		repo   string
		issue  int
	}
	items := make([]trackedIssue, 0)
	for rows.Next() {
		var item trackedIssue
		if rows.Scan(&item.prefix, &item.number, &item.repo, &item.issue) == nil {
			items = append(items, item)
		}
	}
	_ = rows.Close()

	for _, item := range items {
		issue, err := s.githubIssue(ctx, item.repo, item.issue)
		if err != nil {
			continue
		}
		key := fmt.Sprintf("%s#%d", item.repo, item.issue)
		labels := make([]string, 0, len(issue.Labels))
		for _, label := range issue.Labels {
			labels = append(labels, label.Name)
		}
		assignees := make([]string, 0, len(issue.Assignees))
		for _, assignee := range issue.Assignees {
			assignees = append(assignees, assignee.Login)
		}
		milestone := ""
		if issue.Milestone != nil {
			milestone = issue.Milestone.Title
		}
		current := issueSnapshot{
			title:     issue.Title,
			state:     issue.State,
			labels:    strings.Join(labels, ","),
			assignees: strings.Join(assignees, ","),
			milestone: milestone,
			comments:  issue.Comments,
		}

		s.watchMu.Lock()
		previous, known := s.issueSnapshots[key]
		s.issueSnapshots[key] = current
		s.watchMu.Unlock()
		if !known || previous == current {
			continue
		}
		text := fmt.Sprintf("Обращение %s-%03d обновлено\n%s\nСтатус: %s\n%s",
			item.prefix, item.number, issue.Title, issue.State, issue.HTMLURL)
		if previous.comments < current.comments {
			comments, err := s.githubComments(ctx, item.repo, item.issue)
			if err == nil && len(comments) > 0 {
				last := comments[len(comments)-1]
				body := truncateRunes(last.Body, 700)
				text = fmt.Sprintf("Новый комментарий к %s-%03d от @%s:\n%s\n%s",
					item.prefix, item.number, last.User.Login, body, issue.HTMLURL)
			}
		} else if previous.state != current.state {
			text = fmt.Sprintf("%s-%03d: статус изменён на %s\n%s\n%s",
				item.prefix, item.number, current.state, issue.Title, issue.HTMLURL)
		}
		s.notifyAuthor(ctx, item.prefix, item.number, text)
	}
}
