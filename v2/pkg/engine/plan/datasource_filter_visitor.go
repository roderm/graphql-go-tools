package plan

import (
	"errors"
	"fmt"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astvisitor"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

func FilterDataSources(operation, definition *ast.Document, report *operationreport.Report, dataSources []DataSourceConfiguration) ([]DataSourceConfiguration, error) {
	usedDataSources, err := findBestDataSourceSet(operation, definition, report, dataSources)
	if err != nil {
		return nil, err
	}

	filtered := make([]DataSourceConfiguration, 0, len(usedDataSources))
	for _, ds := range usedDataSources {
		filtered = append(filtered, ds.DataSource)
	}

	return filtered, nil
}

type UsedNode struct {
	TypeName  string
	FieldName string
}

type UsedDataSourceConfiguration struct {
	DataSource DataSourceConfiguration
	UsedNodes  []*UsedNode
}

type findUsedDataSourceVisitor struct {
	operation   *ast.Document
	definition  *ast.Document
	walker      *astvisitor.Walker
	dataSources []*UsedDataSourceConfiguration
	err         error
}

func (v *findUsedDataSourceVisitor) EnterField(ref int) {
	typeName := v.walker.EnclosingTypeDefinition.NameString(v.definition)
	fieldName := v.operation.FieldNameUnsafeString(ref)
	found := false
	for _, v := range v.dataSources {
		ds := v.DataSource
		if ds.HasRootNode(typeName, fieldName) || ds.HasChildNode(typeName, fieldName) {
			v.UsedNodes = append(v.UsedNodes, &UsedNode{
				TypeName:  typeName,
				FieldName: fieldName,
			})
			found = true
			break
		}
	}

	if !found {
		v.err = &errOperationFieldNotResolved{TypeName: typeName, FieldName: fieldName}
	}
}

type errOperationFieldNotResolved struct {
	TypeName  string
	FieldName string
}

func (e *errOperationFieldNotResolved) Error() string {
	return fmt.Sprintf("could not resolve %s.%s", e.TypeName, e.FieldName)
}

func findUsedDataSources(operation *ast.Document, definition *ast.Document, report *operationreport.Report, dataSources []DataSourceConfiguration) ([]*UsedDataSourceConfiguration, error) {
	if report == nil {
		panic("report can't be nil")
	}
	walker := astvisitor.NewWalker(32)
	dataSourcesToVisit := make([]*UsedDataSourceConfiguration, len(dataSources))
	for ii, v := range dataSources {
		v := v
		dataSourcesToVisit[ii] = &UsedDataSourceConfiguration{
			DataSource: v,
		}
	}
	visitor := &findUsedDataSourceVisitor{
		operation:   operation,
		definition:  definition,
		walker:      &walker,
		dataSources: dataSourcesToVisit,
	}
	walker.RegisterEnterFieldVisitor(visitor)
	walker.Walk(operation, definition, report)
	if report.HasErrors() {
		return nil, report
	}
	if visitor.err != nil {
		return nil, visitor.err
	}
	var usedDataSources []*UsedDataSourceConfiguration
	for _, v := range dataSourcesToVisit {
		if len(v.UsedNodes) > 0 {
			usedDataSources = append(usedDataSources, v)
		}
	}
	return usedDataSources, nil
}

func findBestDataSourceSet(operation *ast.Document, definition *ast.Document, report *operationreport.Report, dataSources []DataSourceConfiguration) ([]*UsedDataSourceConfiguration, error) {
	if report == nil {
		report = &operationreport.Report{}
	}
	planned, err := findUsedDataSources(operation, definition, report, dataSources)
	if err != nil {
		return nil, err
	}
	if len(planned) == 1 {
		return planned, nil
	}
	best := planned
	for excluded := range dataSources {
		subset := dataSourcesSubset(dataSources, excluded)

		result, err := findBestDataSourceSet(operation, definition, report, subset)
		if err != nil {
			var rerr *errOperationFieldNotResolved
			if errors.As(err, &rerr) {
				// We removed a data source that causes the resolution to fail
				continue
			}
			return nil, err
		}
		if result != nil && len(result) < len(best) {
			best = result
		}
	}
	return best, nil
}

func dataSourcesSubset(dataSources []DataSourceConfiguration, exclude int) []DataSourceConfiguration {
	subset := make([]DataSourceConfiguration, 0, len(dataSources)-1)
	subset = append(subset, dataSources[:exclude]...)
	subset = append(subset, dataSources[exclude+1:]...)
	return subset
}
