// Copyright © 2022 Ory Corp
// SPDX-License-Identifier: Apache-2.0

package check_test

import (
	"context"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/ory/keto/internal/check"
	"github.com/ory/keto/internal/check/checkgroup"
	"github.com/ory/keto/internal/namespace"
	"github.com/ory/keto/internal/namespace/ast"
	"github.com/ory/keto/internal/relationtuple"
	"github.com/ory/keto/internal/schema"
	"github.com/ory/keto/ketoapi"
)

var namespaces = []*namespace.Namespace{
	{Name: "doc",
		Relations: []ast.Relation{
			{
				Name: "owner", Params: []string{"subject"}},
			{
				Name: "editor",
				SubjectSetRewrite: &ast.SubjectSetRewrite{
					Children: ast.Children{&ast.ComputedSubjectSet{
						Relation: "owner"}}},
				Params: []string{"ctx"},
			},
			{
				Name: "viewer",
				SubjectSetRewrite: &ast.SubjectSetRewrite{
					Children: ast.Children{
						&ast.ComputedSubjectSet{
							Relation: "editor"},
						&ast.TupleToSubjectSet{
							Relation:                   "parent",
							ComputedSubjectSetRelation: "viewer"}}},
				Params: []string{"ctx"},
			},
		}},
	{Name: "users"},
	{Name: "group",
		Relations: []ast.Relation{{Name: "member", Params: []string{"subject"}}},
	},
	{Name: "level",
		Relations: []ast.Relation{{Name: "member", Params: []string{"subject"}}},
	},
	{Name: "resource",
		Relations: []ast.Relation{
			{Name: "level", Params: []string{"subject"}},
			{Name: "viewer",
				SubjectSetRewrite: &ast.SubjectSetRewrite{
					Children: ast.Children{
						&ast.TupleToSubjectSet{Relation: "owner", ComputedSubjectSetRelation: "member"}}},
				Params: []string{"ctx"}},
			{Name: "owner",
				SubjectSetRewrite: &ast.SubjectSetRewrite{
					Children: ast.Children{
						&ast.TupleToSubjectSet{Relation: "owner", ComputedSubjectSetRelation: "member"}}},
				Params: []string{"ctx"}},
			{Name: "read",
				SubjectSetRewrite: &ast.SubjectSetRewrite{
					Children: ast.Children{
						&ast.ComputedSubjectSet{Relation: "viewer"},
						&ast.ComputedSubjectSet{Relation: "owner"}}},
				Params: []string{"ctx"}},
			{Name: "update",
				SubjectSetRewrite: &ast.SubjectSetRewrite{
					Children: ast.Children{
						&ast.ComputedSubjectSet{Relation: "owner"}}},
				Params: []string{"ctx"}},
			{Name: "delete",
				SubjectSetRewrite: &ast.SubjectSetRewrite{
					Operation: ast.OperatorAnd,
					Children: ast.Children{
						&ast.ComputedSubjectSet{Relation: "owner"},
						&ast.TupleToSubjectSet{
							Relation:                   "level",
							ComputedSubjectSetRelation: "member"}}},
				Params: []string{"ctx"}},
		}},
	{Name: "acl",
		Relations: []ast.Relation{
			{Name: "allow", Params: []string{"subject"}},
			{Name: "deny", Params: []string{"subject"}},
			{Name: "access",
				SubjectSetRewrite: &ast.SubjectSetRewrite{
					Operation: ast.OperatorAnd,
					Children: ast.Children{
						&ast.ComputedSubjectSet{Relation: "allow"},
						&ast.InvertResult{
							Child: &ast.ComputedSubjectSet{Relation: "deny"}}}},
				Params: []string{"ctx"}}}},
}

func insertFixtures(t testing.TB, m relationtuple.Manager, tuples []string) {
	t.Helper()
	relationTuples := make([]*relationtuple.RelationTuple, len(tuples))
	var err error
	for i, tuple := range tuples {
		relationTuples[i] = tupleFromString(t, tuple)
		require.NoError(t, err)
	}
	require.NoError(t, m.WriteRelationTuples(context.Background(), relationTuples...))
}

type path []string

func TestUsersetRewrites(t *testing.T) {
	reg := newDepsProvider(t, namespaces)
	reg.Logger().Logger.SetLevel(logrus.TraceLevel)

	insertFixtures(t, reg.RelationTupleManager(), []string{
		"doc:document#owner@plain_user",       // user owns doc
		"doc:document#owner@users:user",       // user owns doc
		"doc:doc_in_folder#parent@doc:folder", // doc_in_folder is in folder
		"doc:folder#owner@plain_user",         // user owns folder
		"doc:folder#owner@users:user",         // user owns folder

		// Folder hierarchy folder_a -> folder_b -> folder_c -> file
		// and folder_a is owned by user. Then user should have access to file.
		"doc:file#parent@doc:folder_c",
		"doc:folder_c#parent@doc:folder_b",
		"doc:folder_b#parent@doc:folder_a",
		"doc:folder_a#owner@user",

		"group:editors#member@mark",
		"level:superadmin#member@mark",
		"level:superadmin#member@sandy",
		"resource:topsecret#owner@group:editors#",
		"resource:topsecret#level@level:superadmin#",
		"resource:topsecret#owner@mike",

		"acl:document#allow@alice",
		"acl:document#allow@bob",
		"acl:document#allow@mallory",
		"acl:document#deny@mallory",
	})

	testCases := []struct {
		query         string
		expected      checkgroup.Result
		expectedPaths []path
	}{{
		// direct
		query: "doc:document#owner@users:user",
		expected: checkgroup.Result{
			Membership: checkgroup.IsMember,
		},
	}, {
		// userset rewrite
		query: "doc:document#editor@users:user",
		expected: checkgroup.Result{
			Membership: checkgroup.IsMember,
		},
	}, {
		// userset rewrite
		query:    "doc:document#editor@plain_user",
		expected: checkgroup.ResultIsMember,
	}, {
		// transitive userset rewrite
		query:    "doc:document#viewer@users:user",
		expected: checkgroup.ResultIsMember,
	}, {
		query:    "doc:document#editor@nobody",
		expected: checkgroup.ResultNotMember,
	}, {
		query:    "doc:folder#viewer@users:user",
		expected: checkgroup.ResultIsMember,
	}, {
		// tuple to userset
		query:    "doc:doc_in_folder#viewer@users:user",
		expected: checkgroup.ResultIsMember,
	}, {
		// tuple to userset
		query:    "doc:doc_in_folder#viewer@plain_user",
		expected: checkgroup.ResultIsMember,
	}, {
		// tuple to userset
		query:    "doc:doc_in_folder#viewer@nobody",
		expected: checkgroup.ResultNotMember,
	}, {
		// tuple to userset
		query:    "doc:another_doc#viewer@user",
		expected: checkgroup.ResultNotMember,
	}, {
		query:    "doc:file#viewer@user",
		expected: checkgroup.ResultIsMember,
	}, {
		query:    "level:superadmin#member@mark",
		expected: checkgroup.ResultIsMember, // mark is both editor and has correct level
	}, {
		query:    "resource:topsecret#owner@mark",
		expected: checkgroup.ResultIsMember, // mark is both editor and has correct level
	}, {
		query:    "resource:topsecret#delete@mark",
		expected: checkgroup.ResultIsMember, // mark is both editor and has correct level
		expectedPaths: []path{
			{"*", "resource:topsecret#delete@mark", "level:superadmin#member@mark"},
			{"*", "resource:topsecret#delete@mark", "resource:topsecret#owner@mark", "group:editors#member@mark"},
		},
	}, {
		query:    "resource:topsecret#update@mike",
		expected: checkgroup.ResultIsMember, // mike owns the resource
	}, {
		query:    "level:superadmin#member@mike",
		expected: checkgroup.ResultNotMember, // mike does not have correct level
	}, {
		query:    "resource:topsecret#delete@mike",
		expected: checkgroup.ResultNotMember, // mike does not have correct level
	}, {
		query:    "resource:topsecret#delete@sandy",
		expected: checkgroup.ResultNotMember, // sandy is not in the editor group
	}, {
		query:         "acl:document#access@alice",
		expected:      checkgroup.ResultIsMember,
		expectedPaths: []path{{"*", "acl:document#access@alice", "acl:document#allow@alice"}},
	}, {
		query:    "acl:document#access@bob",
		expected: checkgroup.ResultIsMember,
	}, {
		query:    "acl:document#allow@mallory",
		expected: checkgroup.ResultIsMember,
	}, {
		query:    "acl:document#access@mallory",
		expected: checkgroup.ResultNotMember, // mallory is also on deny-list
	}}

	t.Run("suite=testcases", func(t *testing.T) {
		ctx := context.Background()
		e := check.NewEngine(reg)
		defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

		for _, tc := range testCases {
			t.Run("case="+tc.query, func(t *testing.T) {
				rt := tupleFromString(t, tc.query)

				res := e.CheckRelationTuple(ctx, rt, 100)
				require.NoError(t, res.Err)
				t.Logf("tree:\n%s", res.Tree)
				assert.Equal(t, tc.expected.Membership, res.Membership)

				if len(tc.expectedPaths) > 0 {
					for _, path := range tc.expectedPaths {
						assertPath(t, path, res.Tree)
					}
				}
			})
		}
	})

	t.Run("suite=one worker", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		e := check.NewEngine(reg)
		// Currently we always only use one worker.
		//check.WithPool(
		//checkgroup.NewPool(
		//	checkgroup.WithContext(ctx),
		//	checkgroup.WithWorkers(1),
		//)),

		rt := tupleFromString(t, "doc:file#viewer@user")
		res := e.CheckRelationTuple(ctx, rt, 100)
		require.NoError(t, res.Err)
		assert.Equal(t, checkgroup.ResultIsMember.Membership, res.Membership)
	})
}

// assertPath asserts that the given path can be found in the tree.
func assertPath(t *testing.T, path path, tree *ketoapi.Tree[*relationtuple.RelationTuple]) {
	require.NotNil(t, tree)
	assert.True(t, hasPath(t, path, tree), "could not find path %s in tree:\n%s", path, tree)
}

func hasPath(t *testing.T, path path, tree *ketoapi.Tree[*relationtuple.RelationTuple]) bool {
	if len(path) == 0 {
		return true
	}
	treeLabel := tree.Label()
	if path[0] != "*" {
		// use tupleFromString to compare against paths with UUIDs.
		tuple := tupleFromString(t, path[0])
		if tuple.String() != treeLabel {
			return false
		}
	}

	if len(path) == 1 {
		return true
	}

	for _, child := range tree.Children {
		if hasPath(t, path[1:], child) {
			return true
		}
	}
	return false
}

var rawnsWithStringParameter = `
import { Namespace, SubjectSet, Context } from '@ory/keto-namespace-types';

class Perm implements Namespace {}
class User implements Namespace {}
class Role implements Namespace {
	related: {
	  perms: (Perm | SubjectSet<Role, "perms">)[]
	  owners: Group[]
	  policies: Policy[]
	}

	permits = {
	  has_perm: (ctx: Context, perm: string): boolean => this.related.perms.includes(perm),
	  check: (ctx: Context, perm: string): boolean => this.related.owners.traverse((p) => p.permits.check(ctx, perm)) ||
		this.related.policies.traverse((p) => p.permits.check(ctx, perm)),
	  get: (ctx: Context): boolean => this.permits.check(ctx, "roles.get"),
	  }
  }

  class Policy implements Namespace {
	related: {
	  roles: Role[]
	  users: (User | SubjectSet<Group, "members">)[]
	  policies: Policy[]
	}

	permits = {
	  has_perm: (ctx: Context, perm: string): boolean => this.related.users.includes(ctx.subject) &&
		this.related.roles.traverse((p) => p.permits.has_perm(ctx, perm)),
	  check: (ctx: Context, perm: string): boolean => this.related.policies.traverse((p) => p.permits.has_perm(ctx, perm)),
	  get: (ctx: Context): boolean => this.permits.check(ctx, "policies.get"),
	}
  }

  class Group implements Namespace {
	related: {
	  parents: Group[]
	  members: (User | SubjectSet<Group, "members">)[]
	  policies: Policy[]
	}

	permits = {
	  check: (ctx: Context, perm: string): boolean => this.related.parents.traverse((p) => p.permits.check(ctx, perm)) ||
		this.related.policies.traverse((p) => p.permits.has_perm(ctx, perm)),
	}
  }
`

func TestUsersetRewritesWithStringParameter(t *testing.T) {
	nss, err := schema.Parse(rawnsWithStringParameter)
	require.True(t, err == nil)
	namespaces := make([]*namespace.Namespace, len(nss))
	for i, ns := range nss {
		ns := ns
		namespaces[i] = &ns
	}

	reg := newDepsProvider(t, namespaces)
	reg.Logger().Logger.SetLevel(logrus.TraceLevel)

	insertFixtures(t, reg.RelationTupleManager(), []string{
		"Role:viewer#perms@roles.get",
		"Role:viewer#owners@Group:editors#",
		"Role:viewer#policies@Policy:role_viewer1#",

		"Policy:role_viewer1#roles@Role:viewer#",
		"Policy:role_viewer1#users@User:bob#",
		"Policy:role_viewer1#policies@Policy:role_viewer1#",

		"Group:editors#members@User:mark#",
		"Group:editors#policies@Policy:role_viewer2#",

		"Policy:role_viewer2#roles@Role:viewer#",
		"Policy:role_viewer2#users@User:sandy#",
	})

	testCases := []struct {
		query         string
		expected      checkgroup.Result
		expectedPaths []path
	}{{
		query:    "Role:viewer#get@User:sandy",
		expected: checkgroup.ResultIsMember,
	}, {
		query:    "Role:viewer#get@User:bob",
		expected: checkgroup.ResultIsMember,
	}, {
		query:    "Role:viewer#get@User:mark",
		expected: checkgroup.ResultNotMember,
	}, {
		query:    "Policy:role_viewer1#get@User:bob",
		expected: checkgroup.ResultNotMember,
	}}

	t.Run("suite=testcases", func(t *testing.T) {
		ctx := context.Background()
		e := check.NewEngine(reg)
		defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

		for _, tc := range testCases {
			t.Run("case="+tc.query, func(t *testing.T) {
				rt := tupleFromString(t, tc.query)

				res := e.CheckRelationTuple(ctx, rt, 100)
				require.NoError(t, res.Err)
				t.Logf("tree:\n%s", res.Tree)
				assert.Equal(t, tc.expected.Membership, res.Membership)

				if len(tc.expectedPaths) > 0 {
					for _, path := range tc.expectedPaths {
						assertPath(t, path, res.Tree)
					}
				}
			})
		}
	})

	t.Run("suite=one worker", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		e := check.NewEngine(reg)
		// Currently we always only use one worker.
		//check.WithPool(
		//checkgroup.NewPool(
		//	checkgroup.WithContext(ctx),
		//	checkgroup.WithWorkers(1),
		//)),

		rt := tupleFromString(t, "Role:viewer#get@User:sandy")
		res := e.CheckRelationTuple(ctx, rt, 100)
		require.NoError(t, res.Err)
		assert.Equal(t, checkgroup.ResultIsMember.Membership, res.Membership)
	})
}
