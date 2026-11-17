package chores

import (
	"context"
	"fmt"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

type doers struct {
	all    []int64
	chosen map[int64][]int64
}

func loadDoers(ctx context.Context, q *dbq.Queries, householdID int64) (doers, error) {
	people, err := q.ListPeople(ctx, householdID)
	if err != nil {
		return doers{}, fmt.Errorf("list people: %w", err)
	}
	rows, err := q.ListChorePeople(ctx, householdID)
	if err != nil {
		return doers{}, fmt.Errorf("chore people: %w", err)
	}
	d := doers{chosen: map[int64][]int64{}}
	for _, p := range people {
		d.all = append(d.all, p.ID)
	}
	for _, r := range rows {
		d.chosen[r.ChoreID] = append(d.chosen[r.ChoreID], r.PersonID)
	}
	return d, nil
}

func (d doers) of(c dbq.Chore) []int64 {
	switch {
	case c.Mode != ModeEach:
		return nil
	case c.ForEveryone == 1:
		return d.all
	}
	return d.chosen[c.ID]
}

type slot struct {
	assignee  *int64
	forPerson int64
}

func slots(c dbq.Chore, people []int64) []slot {
	if c.Mode != ModeEach {
		return []slot{{assignee: c.AssigneeID}}
	}
	out := make([]slot, 0, len(people))
	for _, id := range people {
		out = append(out, slot{assignee: &id, forPerson: id})
	}
	return out
}

// Earns is what a person of kind ("adult" or "child") gets for a chore.
func Earns(kind string, points, pence, adultPoints int64) (int64, int64) {
	if kind == "adult" {
		return adultPoints, 0
	}
	return points, pence
}
