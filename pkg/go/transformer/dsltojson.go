package transformer

import (
	"fmt"
	"strings"

	"github.com/antlr4-go/antlr/v4"
	"github.com/hashicorp/go-multierror"
	pb "github.com/openfga/api/proto/openfga/v1"
	parser "github.com/openfga/language/pkg/go/gen"
	"google.golang.org/protobuf/encoding/protojson"
)

type RelationDefinitionOperator string

const (
	RELATION_DEFINITION_OPERATOR_NONE    RelationDefinitionOperator = ""
	RELATION_DEFINITION_OPERATOR_OR      RelationDefinitionOperator = "or"
	RELATION_DEFINITION_OPERATOR_AND     RelationDefinitionOperator = "and"
	RELATION_DEFINITION_OPERATOR_BUT_NOT RelationDefinitionOperator = "but not"
)

// OpenFGA DSL Listener

type relation struct {
	Name     string
	Rewrites []*pb.Userset
	Operator RelationDefinitionOperator
	TypeInfo pb.RelationTypeInfo
}

type OpenFgaDslListener struct {
	*parser.BaseOpenFGAParserListener

	authorizationModel pb.AuthorizationModel
	currentTypeDef     *pb.TypeDefinition
	currentRelation    *relation
	currentCondition   *pb.Condition
}

func newOpenFgaDslListener() *OpenFgaDslListener {
	return new(OpenFgaDslListener)
}

func (l *OpenFgaDslListener) EnterMain(_ctx *parser.MainContext) {
	l.authorizationModel.Conditions = map[string]*pb.Condition{}
}

func (l *OpenFgaDslListener) ExitModelHeader(ctx *parser.ModelHeaderContext) {
	if ctx.GetSchemaVersion() != nil {
		l.authorizationModel.SchemaVersion = ctx.GetSchemaVersion().GetText()
	}
}

func (l *OpenFgaDslListener) EnterTypeDef(ctx *parser.TypeDefContext) {
	if ctx.GetTypeName() == nil {
		return
	}

	l.currentTypeDef = &pb.TypeDefinition{
		Type:      ctx.GetTypeName().GetText(),
		Relations: map[string]*pb.Userset{},
		Metadata: &pb.Metadata{
			Relations: map[string]*pb.RelationMetadata{},
		},
	}
}

func (l *OpenFgaDslListener) EnterConditions(ctx *parser.ConditionsContext) {
	l.authorizationModel.Conditions = map[string]*pb.Condition{}
}

func (l *OpenFgaDslListener) EnterCondition(ctx *parser.ConditionContext) {
	if ctx.ConditionName() == nil {
		return
	}

	conditionName := ctx.ConditionName().GetText()
	if l.authorizationModel.Conditions[conditionName] != nil {
		ctx.GetParser().NotifyErrorListeners(
			fmt.Sprintf("condition '%s' is already defined in the model", conditionName),
			ctx.ConditionName().GetStart(),
			nil)
	}

	l.currentCondition = &pb.Condition{
		Name:       conditionName,
		Expression: "",
		Parameters: map[string]*pb.ConditionParamTypeRef{},
	}
}

func (l *OpenFgaDslListener) ExitConditionParameter(ctx *parser.ConditionParameterContext) {
	if ctx.ParameterName() == nil || ctx.ParameterType() == nil {
		return
	}

	parameterName := ctx.ParameterName().GetText()
	if l.currentCondition.GetParameters()[parameterName] != nil {
		ctx.GetParser().NotifyErrorListeners(
			fmt.Sprintf("parameter '%s' is already defined in the condition '%s'", parameterName, l.currentCondition.GetName()),
			ctx.ParameterName().GetStart(),
			nil)
	}

	paramContainer := ctx.ParameterType().CONDITION_PARAM_CONTAINER()
	typeNameString := ctx.ParameterType().GetText()
	var genericName *pb.ConditionParamTypeRef_TypeName
	if paramContainer != nil {
		typeNameString = paramContainer.GetText()
		genericType := ctx.ParameterType().CONDITION_PARAM_TYPE()
		if genericType != nil {
			genericString := ctx.ParameterType().CONDITION_PARAM_TYPE().GetText()
			genericName = new(pb.ConditionParamTypeRef_TypeName)
			*genericName = pb.ConditionParamTypeRef_TypeName(pb.ConditionParamTypeRef_TypeName_value[fmt.Sprintf("TYPE_NAME_%s", strings.ToUpper(genericString))])
		}
	}

	typeName := new(pb.ConditionParamTypeRef_TypeName)
	*typeName = pb.ConditionParamTypeRef_TypeName(pb.ConditionParamTypeRef_TypeName_value[fmt.Sprintf("TYPE_NAME_%s", strings.ToUpper(typeNameString))])
	conditionParamTypeRef := &pb.ConditionParamTypeRef{
		TypeName:     *typeName,
		GenericTypes: []*pb.ConditionParamTypeRef{},
	}

	if genericName != nil {
		conditionParamTypeRef.GenericTypes = append(conditionParamTypeRef.GenericTypes, &pb.ConditionParamTypeRef{
			TypeName: *genericName,
		})
	}

	l.currentCondition.Parameters[parameterName] = conditionParamTypeRef
}

func (l *OpenFgaDslListener) ExitConditionExpression(ctx *parser.ConditionExpressionContext) {
	l.currentCondition.Expression = strings.TrimRight(ctx.GetText(), "\n")
}

func (l *OpenFgaDslListener) ExitCondition(ctx *parser.ConditionContext) {
	if l.currentCondition != nil {
		l.authorizationModel.Conditions[l.currentCondition.Name] = l.currentCondition

		l.currentCondition = nil
	}
}

func (l *OpenFgaDslListener) ExitTypeDef(_ctx *parser.TypeDefContext) {
	if l.currentTypeDef == nil || l.currentTypeDef.Type == "" {
		return
	}

	if len(l.currentTypeDef.Metadata.Relations) == 0 {
		l.currentTypeDef.Metadata = nil
	}

	l.authorizationModel.TypeDefinitions = append(l.authorizationModel.TypeDefinitions, l.currentTypeDef)

	l.currentTypeDef = nil
}

func (l *OpenFgaDslListener) EnterRelationDeclaration(_ctx *parser.RelationDeclarationContext) {
	l.currentRelation = &relation{
		Rewrites: []*pb.Userset{},
		TypeInfo: pb.RelationTypeInfo{DirectlyRelatedUserTypes: []*pb.RelationReference{}},
	}
}

func (l *OpenFgaDslListener) ExitRelationDeclaration(ctx *parser.RelationDeclarationContext) {
	if ctx.RelationName() == nil {
		return
	}

	relationName := ctx.RelationName().GetText()

	if l.currentRelation.Rewrites == nil || len(l.currentRelation.Rewrites) == 0 {
		return
	}

	var relationDef *pb.Userset

	if len(l.currentRelation.Rewrites) == 1 {
		relationDef = l.currentRelation.Rewrites[0]
	} else {
		switch l.currentRelation.Operator {
		case RELATION_DEFINITION_OPERATOR_OR:
			relationDef = &pb.Userset{
				Userset: &pb.Userset_Union{
					Union: &pb.Usersets{
						Child: l.currentRelation.Rewrites,
					},
				},
			}
		case RELATION_DEFINITION_OPERATOR_AND:
			relationDef = &pb.Userset{
				Userset: &pb.Userset_Intersection{
					Intersection: &pb.Usersets{
						Child: l.currentRelation.Rewrites,
					},
				},
			}
		case RELATION_DEFINITION_OPERATOR_BUT_NOT:
			relationDef = &pb.Userset{
				Userset: &pb.Userset_Difference{
					Difference: &pb.Difference{
						Base:     l.currentRelation.Rewrites[0],
						Subtract: l.currentRelation.Rewrites[1],
					},
				},
			}
		}
	}

	if relationDef != nil {
		if l.currentTypeDef.Relations[relationName] != nil {
			ctx.GetParser().NotifyErrorListeners(
				fmt.Sprintf("'%s' is already defined in '%s'", relationName, l.currentTypeDef.Type),
				ctx.RelationName().GetStart(),
				nil)
		}

		l.currentTypeDef.Relations[relationName] = relationDef
		directlyRelatedUserTypes := l.currentRelation.TypeInfo.GetDirectlyRelatedUserTypes()
		l.currentTypeDef.Metadata.Relations[relationName] = &pb.RelationMetadata{DirectlyRelatedUserTypes: directlyRelatedUserTypes}
	}

	l.currentRelation = nil
}

func (l *OpenFgaDslListener) EnterRelationDefDirectAssignment(_ctx *parser.RelationDefDirectAssignmentContext) {
	l.currentRelation.TypeInfo = pb.RelationTypeInfo{DirectlyRelatedUserTypes: []*pb.RelationReference{}}
}

func (l *OpenFgaDslListener) ExitRelationDefDirectAssignment(_ctx *parser.RelationDefDirectAssignmentContext) {
	partialRewrite := &pb.Userset{Userset: &pb.Userset_This{}}

	l.currentRelation.Rewrites = append(l.currentRelation.Rewrites, partialRewrite)
}

func (l *OpenFgaDslListener) ExitRelationDefTypeRestriction(ctx *parser.RelationDefTypeRestrictionContext) {
	baseRestriction := ctx.RelationDefTypeRestrictionBase()
	if baseRestriction == nil {
		return
	}

	_type := baseRestriction.GetRelationDefTypeRestrictionType()
	usersetRestriction := baseRestriction.GetRelationDefTypeRestrictionRelation()
	wildcardRestriction := baseRestriction.GetRelationDefTypeRestrictionWildcard()
	conditionName := ctx.ConditionName()

	relationRef := &pb.RelationReference{}
	if _type != nil {
		relationRef.Type = _type.GetText()
	}

	if conditionName != nil {
		relationRef.Condition = conditionName.GetText()
	}

	if usersetRestriction != nil {
		relationRef.RelationOrWildcard = &pb.RelationReference_Relation{
			Relation: usersetRestriction.GetText(),
		}
	}

	if wildcardRestriction != nil {
		relationRef.RelationOrWildcard = &pb.RelationReference_Wildcard{Wildcard: &pb.Wildcard{}}
	}

	l.currentRelation.TypeInfo.DirectlyRelatedUserTypes = append(l.currentRelation.TypeInfo.DirectlyRelatedUserTypes, relationRef)
}

func (l *OpenFgaDslListener) ExitRelationDefRewrite(ctx *parser.RelationDefRewriteContext) {
	var partialRewrite *pb.Userset
	computedUserset := &pb.ObjectRelation{
		Relation: ctx.GetRewriteComputedusersetName().GetText(),
	}

	if ctx.GetRewriteTuplesetName() == nil {
		partialRewrite = &pb.Userset{Userset: &pb.Userset_ComputedUserset{
			ComputedUserset: computedUserset,
		}}
	} else {
		partialRewrite = &pb.Userset{Userset: &pb.Userset_TupleToUserset{
			TupleToUserset: &pb.TupleToUserset{
				ComputedUserset: computedUserset,
				Tupleset: &pb.ObjectRelation{
					Relation: ctx.GetRewriteTuplesetName().GetText(),
				},
			},
		}}
	}

	l.currentRelation.Rewrites = append(l.currentRelation.Rewrites, partialRewrite)
}

func (l *OpenFgaDslListener) EnterRelationDefPartials(ctx *parser.RelationDefPartialsContext) {
	if len(ctx.AllOR()) > 0 {
		l.currentRelation.Operator = RELATION_DEFINITION_OPERATOR_OR
	} else if len(ctx.AllAND()) > 0 {
		l.currentRelation.Operator = RELATION_DEFINITION_OPERATOR_AND
	} else if len(ctx.AllBUT_NOT()) > 0 {
		l.currentRelation.Operator = RELATION_DEFINITION_OPERATOR_BUT_NOT
	}
}

//// Error Handling

type OpenFgaDslSyntaxErrorMetadata struct {
	symbol      string
	start, stop int
}

type OpenFgaDslSyntaxError struct {
	line, column int
	msg          string
	metadata     *OpenFgaDslSyntaxErrorMetadata
	e            antlr.RecognitionException //nolint:unused
}

func (err *OpenFgaDslSyntaxError) Error() string {
	return fmt.Sprintf("syntax error at line=%d, column=%d: %s", err.line, err.column, err.msg)
}

type OpenFgaDslSyntaxMultipleError multierror.Error

func (err *OpenFgaDslSyntaxMultipleError) Error() string {
	errors := err.Errors

	pluralS := ""
	if len(errors) > 1 {
		pluralS = "s"
	}

	errorsString := []string{}
	for _, item := range errors {
		errorsString = append(errorsString, item.Error())
	}

	return fmt.Sprintf("%d error%s occurred:\n\t* %s\n\n", len(errors), pluralS, strings.Join(errorsString, "\n\t* "))
}

type OpenFgaDslErrorListener struct {
	*antlr.DefaultErrorListener // Embed default which ensures we fit the interface
	Errors                      *multierror.Error
}

func newOpenFgaDslErrorListener() *OpenFgaDslErrorListener {
	return new(OpenFgaDslErrorListener)
}

func (c *OpenFgaDslErrorListener) SyntaxError(recognizer antlr.Recognizer, offendingSymbol interface{}, line, column int, msg string, e antlr.RecognitionException) {
	var metadata *OpenFgaDslSyntaxErrorMetadata
	if offendingSymbol != nil {
		symbol := offendingSymbol.(*antlr.CommonToken)
		metadata = &OpenFgaDslSyntaxErrorMetadata{
			symbol: symbol.GetText(),
			start:  symbol.GetStart(),
			stop:   symbol.GetStop(),
		}
	}

	c.Errors = multierror.Append(c.Errors, &OpenFgaDslSyntaxError{
		line:     line,
		column:   column,
		msg:      msg,
		metadata: metadata,
	})
}

///

func ParseDSL(data string) (*OpenFgaDslListener, *OpenFgaDslErrorListener) {
	cleanedLines := []string{}
	for _, line := range strings.Split(data, "\n") {
		cleanedLines = append(cleanedLines, strings.TrimRight(line, " "))
	}
	cleanedData := strings.TrimRight(strings.Join(cleanedLines, "\n"), "\n")

	inputStream := antlr.NewInputStream(cleanedData)

	errorListener := newOpenFgaDslErrorListener()

	// Create the Lexer
	lexer := parser.NewOpenFGALexer(inputStream)
	lexer.RemoveErrorListeners()
	lexer.AddErrorListener(errorListener)
	stream := antlr.NewCommonTokenStream(lexer, antlr.TokenDefaultChannel)

	// Create the Parser
	fgaParser := parser.NewOpenFGAParser(stream)
	fgaParser.RemoveErrorListeners()
	fgaParser.AddErrorListener(errorListener)

	listener := newOpenFgaDslListener()
	antlr.ParseTreeWalkerDefault.Walk(listener, fgaParser.Main())

	return listener, errorListener
}

// TransformDSLToProto - Converts models authored in FGA DSL syntax to the OpenFGA Authorization Model Protobuf format
func TransformDSLToProto(data string) (*pb.AuthorizationModel, error) {
	listener, errorListener := ParseDSL(data)

	if errorListener.Errors != nil {
		return nil, errorListener.Errors
	}

	return &listener.authorizationModel, nil
}

// MustTransformDSLToProto - Calls TransformDSLToProto - panics if the error fails
func MustTransformDSLToProto(data string) *pb.AuthorizationModel {
	model, err := TransformDSLToProto(data)
	if err != nil {
		panic(err)
	}

	return model
}

// TransformDSLToJSON - Converts models authored in FGA DSL syntax to the json syntax accepted by the OpenFGA API
func TransformDSLToJSON(data string) (string, error) {
	model, err := TransformDSLToProto(data)
	if err != nil {
		return "", err
	}

	bytes, err := protojson.Marshal(model)
	if err != nil {
		return "", fmt.Errorf("failed to marshal due to %w", err)
	}

	return string(bytes), nil
}

// MustTransformDSLToJSON - Calls TransformDSLToJSON - panics if the error fails
func MustTransformDSLToJSON(data string) string {
	jsonString, err := TransformDSLToJSON(data)
	if err != nil {
		panic(err)
	}

	return jsonString
}
