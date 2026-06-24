package dbconn

import "testing"

func TestInferColumnRelationships(t *testing.T) {
	idx := schemaTableIndex{
		Owner: "SCHOOL",
		Tables: map[string]TableRef{
			"STUDENTS":     {Schema: "SCHOOL", Name: "STUDENTS"},
			"COURSES":      {Schema: "SCHOOL", Name: "COURSES"},
			"ENROLLMENTS":  {Schema: "SCHOOL", Name: "ENROLLMENTS"},
			"DEPARTMENTS":  {Schema: "SCHOOL", Name: "DEPARTMENTS"},
		},
		Columns: map[string][]string{
			"STUDENTS":    {"STUDENT_ID", "DEPT_ID", "FIRST_NAME"},
			"COURSES":     {"COURSE_ID", "COURSE_NAME"},
			"ENROLLMENTS": {"ENROLLMENT_ID", "STUDENT_ID", "COURSE_ID"},
			"DEPARTMENTS": {"DEPT_ID", "DEPT_NAME"},
		},
		ColTypes: columnTypes{
			"STUDENTS": {
				"STUDENT_ID": {DataType: "NUMBER"},
				"DEPT_ID":    {DataType: "NUMBER"},
			},
			"COURSES": {
				"COURSE_ID": {DataType: "NUMBER"},
			},
			"ENROLLMENTS": {
				"ENROLLMENT_ID": {DataType: "NUMBER"},
				"STUDENT_ID":    {DataType: "NUMBER"},
				"COURSE_ID":     {DataType: "NUMBER"},
			},
			"DEPARTMENTS": {
				"DEPT_ID": {DataType: "NUMBER"},
			},
		},
		PKCols: map[string]map[string]bool{
			"STUDENTS":    {"STUDENT_ID": true},
			"COURSES":     {"COURSE_ID": true},
			"ENROLLMENTS": {"ENROLLMENT_ID": true},
			"DEPARTMENTS": {"DEPT_ID": true},
		},
		ColIndex: map[string][]string{
			"STUDENT_ID": {"STUDENTS", "ENROLLMENTS"},
			"COURSE_ID":  {"COURSES", "ENROLLMENTS"},
			"DEPT_ID":    {"STUDENTS", "DEPARTMENTS"},
		},
	}
	selected := []TableRef{{Schema: "SCHOOL", Name: "ENROLLMENTS"}}
	inferred := InferColumnRelationships(idx, selected, nil)
	if len(inferred) < 2 {
		t.Fatalf("inferred = %#v, want at least STUDENTS and COURSES", inferred)
	}
	parents := map[string]bool{}
	for _, e := range inferred {
		parents[e.ParentTable] = true
	}
	if !parents["STUDENTS"] || !parents["COURSES"] {
		t.Fatalf("parents = %#v", parents)
	}
}

func TestInferShortStringPKRelationship(t *testing.T) {
	codeLen := int64(10)
	idx := schemaTableIndex{
		Owner: "APP",
		Tables: map[string]TableRef{
			"COUNTRIES": {Schema: "APP", Name: "COUNTRIES"},
			"CUSTOMERS": {Schema: "APP", Name: "CUSTOMERS"},
		},
		Columns: map[string][]string{
			"COUNTRIES": {"COUNTRY_CODE", "COUNTRY_NAME"},
			"CUSTOMERS": {"CUSTOMER_ID", "COUNTRY_CODE"},
		},
		ColTypes: columnTypes{
			"COUNTRIES": {
				"COUNTRY_CODE": {DataType: "CHAR", CharMaxLen: &codeLen},
			},
			"CUSTOMERS": {
				"CUSTOMER_ID":  {DataType: "NUMBER"},
				"COUNTRY_CODE": {DataType: "CHAR", CharMaxLen: &codeLen},
			},
		},
		PKCols: map[string]map[string]bool{
			"COUNTRIES": {"COUNTRY_CODE": true},
		},
		ColIndex: map[string][]string{
			"COUNTRY_CODE": {"COUNTRIES", "CUSTOMERS"},
		},
	}
	inferred := InferColumnRelationships(idx, []TableRef{{Name: "CUSTOMERS"}}, nil)
	if len(inferred) != 1 {
		t.Fatalf("inferred = %#v, want one string PK link", inferred)
	}
	e := inferred[0]
	if e.ParentTable != "COUNTRIES" || e.ChildColumn != "COUNTRY_CODE" || e.Confidence != "medium" {
		t.Fatalf("inferred = %#v", e)
	}
}

func TestInferSkipsDeclaredFK(t *testing.T) {
	idx := schemaTableIndex{
		Owner: "SCHOOL",
		Tables: map[string]TableRef{
			"STUDENTS":    {Schema: "SCHOOL", Name: "STUDENTS"},
			"ENROLLMENTS": {Schema: "SCHOOL", Name: "ENROLLMENTS"},
		},
		Columns: map[string][]string{
			"STUDENTS":    {"STUDENT_ID"},
			"ENROLLMENTS": {"STUDENT_ID"},
		},
		ColTypes: columnTypes{
			"STUDENTS":    {"STUDENT_ID": {DataType: "NUMBER"}},
			"ENROLLMENTS": {"STUDENT_ID": {DataType: "NUMBER"}},
		},
		PKCols: map[string]map[string]bool{
			"STUDENTS": {"STUDENT_ID": true},
		},
		ColIndex: map[string][]string{
			"STUDENT_ID": {"STUDENTS", "ENROLLMENTS"},
		},
	}
	declared := []FKEdge{{
		ChildSchema: "SCHOOL", ChildTable: "ENROLLMENTS", ChildColumn: "STUDENT_ID",
		ParentSchema: "SCHOOL", ParentTable: "STUDENTS", ParentColumn: "STUDENT_ID",
	}}
	inferred := InferColumnRelationships(idx, []TableRef{{Name: "ENROLLMENTS"}}, declared)
	for _, e := range inferred {
		if e.ParentTable == "STUDENTS" && e.ChildColumn == "STUDENT_ID" {
			t.Fatalf("should not duplicate declared FK: %#v", e)
		}
	}
}
