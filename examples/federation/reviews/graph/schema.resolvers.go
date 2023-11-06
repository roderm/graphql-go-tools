package graph

// This file will be automatically regenerated based on the schema, any resolver implementations
// will be copied through when generating and any unknown code will be moved to the end.

import (
	"context"
	"fmt"

	"github.com/wundergraph/graphql-go-tools/examples/federation/reviews/graph/generated"
	"github.com/wundergraph/graphql-go-tools/examples/federation/reviews/graph/model"
)

// Reviews is the resolver for the reviews field.
func (r *productResolver) Reviews(ctx context.Context, obj *model.Product, paging *model.Paging) (*model.ReviewConnection, error) {
	res := &model.ReviewConnection{
		Edges:      []*model.ReviewEdge{},
		TotalCount: new(int),
	}
	for _, review := range reviews {
		if review.Product.Upc == obj.Upc {
			res.Edges = append(res.Edges, &model.ReviewEdge{
				Cursor: fmt.Sprintf("cursor-%s-%s", review.Author.ID, review.Product.Upc),
				Node:   review,
			})
			*res.TotalCount++
		}
	}
	return res, nil
}

// Username is the resolver for the username field.
func (r *userResolver) Username(ctx context.Context, obj *model.User) (string, error) {
	return fmt.Sprintf("User %s", obj.ID), nil
}

// Reviews is the resolver for the reviews field.
func (r *userResolver) Reviews(ctx context.Context, obj *model.User, paging *model.Paging) (*model.ReviewConnection, error) {
	res := &model.ReviewConnection{
		Edges:      []*model.ReviewEdge{},
		TotalCount: new(int),
	}
	for _, review := range reviews {
		if review.Author.ID == obj.ID {
			res.Edges = append(res.Edges, &model.ReviewEdge{
				Cursor: fmt.Sprintf("cursor-%s-%s", review.Author.ID, review.Product.Upc),
				Node:   review,
			})
			*res.TotalCount++
		}
	}
	return res, nil
}

// Product returns generated.ProductResolver implementation.
func (r *Resolver) Product() generated.ProductResolver { return &productResolver{r} }

// User returns generated.UserResolver implementation.
func (r *Resolver) User() generated.UserResolver { return &userResolver{r} }

type productResolver struct{ *Resolver }
type userResolver struct{ *Resolver }
