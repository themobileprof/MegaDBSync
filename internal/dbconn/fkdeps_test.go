package dbconn

import (
	"strings"
	"testing"
)

func TestResolveTableDependenciesNested(t *testing.T) {
	edges := []FKEdge{
		{ChildSchema: "SCHOOL", ChildTable: "ENROLLMENTS", ChildColumn: "STUDENT_ID", ParentSchema: "SCHOOL", ParentTable: "STUDENTS", ParentColumn: "STUDENT_ID"},
		{ChildSchema: "SCHOOL", ChildTable: "ENROLLMENTS", ChildColumn: "COURSE_ID", ParentSchema: "SCHOOL", ParentTable: "COURSES", ParentColumn: "COURSE_ID"},
		{ChildSchema: "SCHOOL", ChildTable: "STUDENTS", ChildColumn: "DEPT_ID", ParentSchema: "SCHOOL", ParentTable: "DEPARTMENTS", ParentColumn: "DEPT_ID"},
	}
	res := ResolveTableDependencies(edges, "SCHOOL", []TableRef{{Name: "ENROLLMENTS"}})
	if len(res.Dependencies) != 3 {
		t.Fatalf("dependencies = %d, want 3", len(res.Dependencies))
	}
	depths := map[string]int{}
	for _, d := range res.Dependencies {
		depths[d.Name] = d.Depth
	}
	if depths["STUDENTS"] != 1 || depths["COURSES"] != 1 || depths["DEPARTMENTS"] != 2 {
		t.Fatalf("depths = %#v", depths)
	}
	order := res.SuggestedMigrationOrder
	if len(order) != 4 {
		t.Fatalf("order = %#v", order)
	}
	if order[len(order)-1] != "SCHOOL.ENROLLMENTS" {
		t.Fatalf("child should be last, order = %#v", order)
	}
	idx := func(name string) int {
		for i, k := range order {
			if strings.HasSuffix(k, name) {
				return i
			}
		}
		return -1
	}
	if idx("DEPARTMENTS") > idx("STUDENTS") || idx("STUDENTS") > idx("ENROLLMENTS") {
		t.Fatalf("parents should precede children, order = %#v", order)
	}
}

func TestFormatTableFilter(t *testing.T) {
	got := FormatTableFilter([]TableRef{
		{Schema: "SCHOOL", Name: "STUDENTS"},
		{Schema: "SCHOOL", Name: "COURSES"},
	})
	if got != "SCHOOL.COURSES, SCHOOL.STUDENTS" {
		t.Fatalf("filter = %q", got)
	}
}
