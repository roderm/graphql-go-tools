package plan

import (
	"fmt"
	"strings"

	"github.com/jensneuse/abstractlogger"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astnormalization"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astprinter"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astvisitor"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

type Planner struct {
	config               Configuration
	configurationWalker  *astvisitor.Walker
	configurationVisitor *configurationVisitor
	planningWalker       *astvisitor.Walker
	planningVisitor      *Visitor

	prepareOperationWalker *astvisitor.Walker
}

// NewPlanner creates a new Planner from the Configuration
// NOTE: All stateful DataSources should be initiated with the same context.Context object provided to the PlannerFactory.
// The context.Context object is used to determine the lifecycle of stateful DataSources
// It's important to note that stateful DataSources must be closed when they are no longer being used
// Stateful DataSources could be those that initiate a WebSocket connection to an origin, a database client, a streaming client, etc...
// To ensure that there are no memory leaks, it's therefore important to add a cancel func or timeout to the context.Context
// At the time when the resolver and all operations should be garbage collected, ensure to first cancel or timeout the ctx object
// If you don't cancel the context.Context, the goroutines will run indefinitely and there's no reference left to stop them
func NewPlanner(config Configuration) (*Planner, error) {
	if config.Logger == nil {
		config.Logger = abstractlogger.Noop{}
	}

	dsIDs := make(map[string]struct{}, len(config.DataSources))
	for _, ds := range config.DataSources {
		if _, ok := dsIDs[ds.Id()]; ok {
			return nil, fmt.Errorf("duplicate datasource id: %s", ds.Id())
		}
		dsIDs[ds.Id()] = struct{}{}
	}

	// prepare operation walker handles internal normalization for planner
	prepareOperationWalker := astvisitor.NewWalker(48)
	astnormalization.InlineFragmentAddOnType(&prepareOperationWalker)

	// configuration
	configurationWalker := astvisitor.NewWalker(48)
	configVisitor := &configurationVisitor{
		walker:              &configurationWalker,
		fieldConfigurations: config.Fields,
	}

	configurationWalker.RegisterEnterDocumentVisitor(configVisitor)
	configurationWalker.RegisterFieldVisitor(configVisitor)
	configurationWalker.RegisterEnterOperationVisitor(configVisitor)
	configurationWalker.RegisterSelectionSetVisitor(configVisitor)

	// planning

	planningWalker := astvisitor.NewWalker(48)
	planningVisitor := &Visitor{
		Walker:                       &planningWalker,
		fieldConfigs:                 map[int]*FieldConfiguration{},
		disableResolveFieldPositions: config.DisableResolveFieldPositions,
	}

	p := &Planner{
		config:                 config,
		configurationWalker:    &configurationWalker,
		configurationVisitor:   configVisitor,
		planningWalker:         &planningWalker,
		planningVisitor:        planningVisitor,
		prepareOperationWalker: &prepareOperationWalker,
	}

	return p, nil
}

func (p *Planner) SetConfig(config Configuration) {
	p.config = config
}

func (p *Planner) SetDebugConfig(config DebugConfiguration) {
	p.config.Debug = config
}

type _opts struct {
	includeQueryPlanInResponse bool
}

type Opts func(*_opts)

func IncludeQueryPlanInResponse() Opts {
	return func(o *_opts) {
		o.includeQueryPlanInResponse = true
	}
}

func (p *Planner) Plan(operation, definition *ast.Document, operationName string, report *operationreport.Report, options ...Opts) (plan Plan) {

	var opts _opts
	for _, opt := range options {
		opt(&opts)
	}

	p.planningVisitor.includeQueryPlans = opts.includeQueryPlanInResponse

	p.selectOperation(operation, operationName, report)
	if report.HasErrors() {
		return
	}

	p.prepareOperation(operation, definition, report)
	if report.HasErrors() {
		return
	}

	// assign hash to each datasource
	for i := range p.config.DataSources {
		p.config.DataSources[i].Hash()
	}

	p.findPlanningPaths(operation, definition, report)
	if report.HasErrors() {
		return nil
	}

	if p.config.Debug.PlanningVisitor {
		p.debugMessage("Planning visitor:")
	}

	// configure planning visitor

	p.planningVisitor.planners = p.configurationVisitor.planners
	p.planningVisitor.Config = p.config
	p.planningVisitor.skipFieldsRefs = p.configurationVisitor.skipFieldsRefs

	p.planningWalker.ResetVisitors()
	p.planningWalker.SetVisitorFilter(p.planningVisitor)
	p.planningWalker.RegisterDocumentVisitor(p.planningVisitor)
	p.planningWalker.RegisterEnterOperationVisitor(p.planningVisitor)
	p.planningWalker.RegisterFieldVisitor(p.planningVisitor)
	p.planningWalker.RegisterSelectionSetVisitor(p.planningVisitor)
	p.planningWalker.RegisterEnterDirectiveVisitor(p.planningVisitor)
	p.planningWalker.RegisterInlineFragmentVisitor(p.planningVisitor)

	for key := range p.planningVisitor.planners {
		if p.config.MinifySubgraphOperations {
			if dataSourceWithMinify, ok := p.planningVisitor.planners[key].Planner().(SubgraphRequestMinifier); ok {
				dataSourceWithMinify.EnableSubgraphRequestMinifier()
			}
		}
		if opts.includeQueryPlanInResponse {
			if plannerWithQueryPlan, ok := p.planningVisitor.planners[key].Planner().(QueryPlanProvider); ok {
				plannerWithQueryPlan.IncludeQueryPlanInFetchConfiguration()
			}
		}
		if plannerWithId, ok := p.planningVisitor.planners[key].Planner().(astvisitor.VisitorIdentifier); ok {
			plannerWithId.SetID(key)
		}
		if plannerWithDebug, ok := p.planningVisitor.planners[key].Debugger(); ok {
			if p.config.Debug.DatasourceVisitor {
				plannerWithDebug.EnableDebug()
			}

			if p.config.Debug.PrintQueryPlans {
				plannerWithDebug.EnableDebugQueryPlanLogging()
			}
		}
		err := p.planningVisitor.planners[key].Register(p.planningVisitor)
		if err != nil {
			report.AddInternalError(err)
			return
		}
	}

	// process the plan

	p.planningWalker.Walk(operation, definition, report)
	if report.HasErrors() {
		return
	}

	return p.planningVisitor.plan
}

func (p *Planner) findPlanningPaths(operation, definition *ast.Document, report *operationreport.Report) {
	dsFilter := NewDataSourceFilter(operation, definition, report)

	if p.config.Debug.EnableNodeSuggestionsSelectionReasons {
		dsFilter.EnableSelectionReasons()
	}

	if p.config.Debug.PrintOperationTransformations {
		p.debugMessage("Initial operation:")
		p.printOperation(operation)
	}

	p.configurationVisitor.debug = p.config.Debug.ConfigurationVisitor
	p.configurationVisitor.suggestionsSelectionReasonsEnabled = p.config.Debug.EnableNodeSuggestionsSelectionReasons

	// set initial suggestions and used data sources
	p.configurationVisitor.dataSources, p.configurationVisitor.nodeSuggestions =
		dsFilter.FilterDataSources(p.config.DataSources, nil)
	if report.HasErrors() {
		return
	}

	if p.config.Debug.PrintNodeSuggestions {
		p.configurationVisitor.nodeSuggestions.printNodes("\n\nInitial node suggestions:\n\n")
	}

	p.configurationVisitor.secondaryRun = false
	p.configurationWalker.Walk(operation, definition, report)
	if report.HasErrors() {
		return
	}

	if p.config.Debug.PrintOperationTransformations {
		p.debugMessage("Operation after initial run:")
		p.printOperation(operation)
	}

	if p.config.Debug.PrintPlanningPaths {
		p.debugMessage("Planning paths after initial run")
		p.printRevisitInfo()
		p.printPlanningPaths()
	}

	i := 1
	// secondary runs to add path for the new required fields
	for p.configurationVisitor.shouldRevisit() {
		p.configurationVisitor.secondaryRun = true

		if p.configurationVisitor.hasNewFields {
			// update suggestions for the new required fields
			p.configurationVisitor.dataSources, p.configurationVisitor.nodeSuggestions =
				dsFilter.FilterDataSources(p.config.DataSources, p.configurationVisitor.nodeSuggestions, p.configurationVisitor.nodeSuggestionHints...)
			if report.HasErrors() {
				return
			}

		}

		p.configurationWalker.Walk(operation, definition, report)
		if report.HasErrors() {
			return
		}

		if p.config.Debug.PrintOperationTransformations || p.config.Debug.PrintPlanningPaths || p.config.Debug.PrintNodeSuggestions {
			p.debugMessage(fmt.Sprintf("After run #%d", i))
		}

		if p.config.Debug.PrintOperationTransformations {
			p.debugMessage("Operation with new required fields:")
			p.debugMessage(fmt.Sprintf("Has new fields: %v", p.configurationVisitor.hasNewFields))
			p.printOperation(operation)
		}

		if p.config.Debug.PrintNodeSuggestions {
			p.configurationVisitor.nodeSuggestions.printNodes("\nRecalculated node suggestions:\n")
		}

		if p.config.Debug.PrintPlanningPaths {
			p.printRevisitInfo()
			p.printPlanningPaths()
		}

		i++

		if i > 100 {
			missingPaths := make([]string, 0, len(p.configurationVisitor.missingPathTracker))
			for path := range p.configurationVisitor.missingPathTracker {
				missingPaths = append(missingPaths, path)
			}

			report.AddInternalError(fmt.Errorf("bad datasource configuration - could not plan the operation. missing path: %v", missingPaths))
			return
		}
	}

	// remove unnecessary fragment paths
	hasRemovedPaths := p.removeUnnecessaryFragmentPaths()
	if hasRemovedPaths && p.config.Debug.PrintPlanningPaths {
		p.debugMessage("After removing unnecessary fragment paths")
		p.printPlanningPaths()
	}
}

func (p *Planner) removeUnnecessaryFragmentPaths() (hasRemovedPaths bool) {
	// We add fragment paths on enter selection set of fragments in configurationVisitor
	// It could happen that datasource has a root node for the given fragment type,
	// but we do not select any fields from this fragment
	// So we need to remove all fragment paths that are not prefixes of any other path

	for _, planner := range p.configurationVisitor.planners {
		if planner.RemoveLeafFragmentPaths() {
			hasRemovedPaths = true
		}
	}
	return
}

func (p *Planner) selectOperation(operation *ast.Document, operationName string, report *operationreport.Report) {

	numOfOperations := operation.NumOfOperationDefinitions()
	operationName = strings.TrimSpace(operationName)
	if len(operationName) == 0 && numOfOperations > 1 {
		report.AddExternalError(operationreport.ErrRequiredOperationNameIsMissing())
		return
	}

	if len(operationName) == 0 && numOfOperations == 1 {
		operationName = operation.OperationDefinitionNameString(0)
	}

	if !operation.OperationNameExists(operationName) {
		report.AddExternalError(operationreport.ErrOperationWithProvidedOperationNameNotFound(operationName))
		return
	}

	p.configurationVisitor.operationName = operationName
	p.planningVisitor.OperationName = operationName
}

func (p *Planner) prepareOperation(operation, definition *ast.Document, report *operationreport.Report) {
	p.prepareOperationWalker.Walk(operation, definition, report)
}

func (p *Planner) printOperation(operation *ast.Document) {
	var pp string

	if p.config.Debug.PrintOperationEnableASTRefs {
		pp, _ = astprinter.PrintStringIndentDebug(operation, "  ")
	} else {
		pp, _ = astprinter.PrintStringIndent(operation, "  ")
	}

	fmt.Println(pp)
}

func (p *Planner) printRevisitInfo() {
	fmt.Println("Should revisit:", p.configurationVisitor.shouldRevisit())
	fmt.Println("Has new fields:", p.configurationVisitor.hasNewFields)
	fmt.Println("Has missing paths:", p.configurationVisitor.hasMissingPaths())
	fmt.Println("Has fields waiting for dependency:", p.configurationVisitor.hasFieldsWaitingForDependency())

	p.printMissingPaths()
}

func (p *Planner) printPlanningPaths() {
	p.debugMessage("Planning paths:")
	for i, planner := range p.configurationVisitor.planners {
		fmt.Printf("\nPlanner id: %d\n", i)
		fmt.Println("Planner parent path", planner.ParentPath())
		fmt.Printf("Depends on planner ids: %v\n", planner.ObjectFetchConfiguration().dependsOnFetchIDs)

		requiredFields := planner.RequiredFields()
		if requiredFields != nil && len(*requiredFields) > 0 {
			fmt.Println("Required fields:")
			for _, field := range *requiredFields {
				fmt.Println(field)
			}
		}
		fmt.Println("Paths:")
		planner.ForEachPath(func(path *pathConfiguration) (shouldBreak bool) {
			fmt.Println(path.String())
			return false
		})
		fmt.Println()
	}
}

func (p *Planner) printMissingPaths() {
	if p.configurationVisitor.hasMissingPaths() {
		p.debugMessage("Missing paths:")
		for path := range p.configurationVisitor.missingPathTracker {
			fmt.Println(path)
		}
	}
}

func (p *Planner) debugMessage(msg string) {
	fmt.Printf("\n\n%s\n\n", msg)
}
