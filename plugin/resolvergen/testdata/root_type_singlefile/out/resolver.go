package rootsingle

// THIS CODE WILL BE UPDATED WITH SCHEMA CHANGES. PREVIOUS IMPLEMENTATION FOR SCHEMA CHANGES WILL BE KEPT IN THE COMMENT SECTION. IMPLEMENTATION FOR UNCHANGED SCHEMA WILL BE KEPT.

import (
	"context"

	customresolver "github.com/99designs/gqlgen/plugin/resolvergen/testdata/singlefile/out"
)

type CustomResolverType struct{}

type ResolverRoot struct{ *CustomResolverType }

// Resolver is the resolver for the resolver field.
func (r *queryCustomResolverType) Resolver(ctx context.Context) (*customresolver.Resolver, error) {
	panic("not implemented")
}

// Name is the resolver for the name field.
func (r *resolverCustomResolverType) Name(ctx context.Context, obj *customresolver.Resolver) (string, error) {
	panic("not implemented")
}

// Query returns customresolver.QueryResolver implementation.
func (r ResolverRoot) Query() customresolver.QueryResolver {
	return &queryCustomResolverType{r.CustomResolverType}
}

// Resolver returns customresolver.ResolverResolver implementation.
func (r ResolverRoot) Resolver() customresolver.ResolverResolver {
	return &resolverCustomResolverType{r.CustomResolverType}
}

type queryCustomResolverType struct{ *CustomResolverType }
type resolverCustomResolverType struct{ *CustomResolverType }

// !!! WARNING !!!
// The code below was going to be deleted when updating resolvers. It has been copied here so you have
// one last chance to move it out of harms way if you want. There are two reasons this happens:
//  - When renaming or deleting a resolver the old code will be put in here. You can safely delete
//    it when you're done.
//  - You have helper methods in this file. Move them out to keep these resolver files clean.
/*
	type CustomResolverType struct{}
*/
