import type {
  AuthorizationModel,
  Condition,
  ConditionParamTypeRef,
  ObjectRelation,
  RelationMetadata,
  RelationReference,
  TypeDefinition,
  Userset,
} from "@openfga/sdk";
import * as antlr from "antlr4";
import { ErrorListener, RecognitionException, Recognizer } from "antlr4";
import OpenFGAListener from "../gen/OpenFGAParserListener";
import OpenFGALexer from "../gen/OpenFGALexer";
import OpenFGAParser, {
  ConditionContext,
  ConditionExpressionContext,
  ConditionParameterContext,
  ModelHeaderContext,
  RelationDeclarationContext,
  RelationDefDirectAssignmentContext,
  RelationDefPartialsContext,
  RelationDefRewriteContext,
  RelationDefTypeRestrictionContext,
  TypeDefContext,
  TypeDefsContext,
} from "../gen/OpenFGAParser";
import { DSLSyntaxError, DSLSyntaxSingleError } from "../errors";
import { TypeName } from "@openfga/sdk";

enum RelationDefinitionOperator {
  RELATION_DEFINITION_OPERATOR_NONE = "",
  RELATION_DEFINITION_OPERATOR_OR = "or",
  RELATION_DEFINITION_OPERATOR_AND = "and",
  RELATION_DEFINITION_OPERATOR_BUT_NOT = "but not",
}

type RelationTypeInfo = RelationMetadata;

interface Relation {
  name: string;
  rewrites: Userset[];
  operator: RelationDefinitionOperator;
  typeInfo: RelationTypeInfo;
}

/**
 * This Visitor walks the tree generated by parsers and produces Python code
 *
 * @returns {object}
 */
class OpenFgaDslListener extends OpenFGAListener {
  public authorizationModel: Partial<AuthorizationModel> = {};
  private currentTypeDef: Partial<TypeDefinition> | undefined;
  private currentRelation: Partial<Relation> | undefined;
  private currentCondition: Condition | undefined;

  exitModelHeader = (ctx: ModelHeaderContext) => {
    if (ctx.SCHEMA_VERSION()) {
      this.authorizationModel.schema_version = ctx.SCHEMA_VERSION().getText();
    }
  };

  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  enterTypeDefs = (_ctx: TypeDefsContext) => {
    this.authorizationModel.type_definitions = [];
  };

  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  exitTypeDefs = (_ctx: TypeDefsContext) => {
    if (!this.authorizationModel.type_definitions?.length) {
      delete this.authorizationModel.type_definitions;
    }
  };

  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  enterTypeDef = (ctx: TypeDefContext) => {
    if (!ctx._typeName) {
      return;
    }

    this.currentTypeDef = {
      type: ctx._typeName.text,
      relations: {},
      metadata: { relations: {} },
    };
  };

  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  exitTypeDef = (_ctx: TypeDefContext) => {
    if (!this.currentTypeDef?.type) {
      return;
    }

    if (!Object.keys(this.currentTypeDef?.metadata?.relations || {}).length) {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      this.currentTypeDef!.metadata = null as any;
    }

    this.authorizationModel.type_definitions?.push(this.currentTypeDef as TypeDefinition);
    this.currentTypeDef = undefined;
  };

  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  enterRelationDeclaration = (_ctx: RelationDeclarationContext) => {
    this.currentRelation = {
      rewrites: [],
      typeInfo: { directly_related_user_types: [] },
    };
  };

  exitRelationDeclaration = (ctx: RelationDeclarationContext) => {
    if (!ctx.relationName()) {
      return;
    }

    const relationName = ctx.relationName().getText();
    let relationDef: Userset | undefined;
    const rewrites = this.currentRelation?.rewrites;
    if (!rewrites?.length) {
      return;
    }
    if (rewrites?.length === 1) {
      relationDef = rewrites[0];
    } else {
      switch (this.currentRelation?.operator) {
        case RelationDefinitionOperator.RELATION_DEFINITION_OPERATOR_OR:
          relationDef = {
            union: {
              child: rewrites,
            },
          };
          break;
        case RelationDefinitionOperator.RELATION_DEFINITION_OPERATOR_AND:
          relationDef = {
            intersection: {
              child: rewrites,
            },
          };
          break;
        case RelationDefinitionOperator.RELATION_DEFINITION_OPERATOR_BUT_NOT:
          relationDef = {
            difference: {
              base: rewrites![0],
              subtract: rewrites![1],
            },
          };
          break;
      }
    }
    if (relationDef) {
      // Throw error if same named relation occurs more than once in a relationship definition block
      if (this.currentTypeDef!.relations![relationName]) {
        ctx.parser?.notifyErrorListeners(
          `'${relationName}' is already defined in '${this.currentTypeDef?.type}'`,
          ctx.relationName().start,
          undefined,
        );
      }

      this.currentTypeDef!.relations![relationName] = relationDef;
      const directlyRelatedUserTypes = this.currentRelation?.typeInfo?.directly_related_user_types;
      this.currentTypeDef!.metadata!.relations![relationName] = {
        directly_related_user_types: directlyRelatedUserTypes,
      };
    }

    this.currentRelation = undefined;
  };

  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  enterRelationDefDirectAssignment = (_ctx: RelationDefDirectAssignmentContext) => {
    this.currentRelation!.typeInfo = { directly_related_user_types: [] };
  };

  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  exitRelationDefDirectAssignment = (_ctx: RelationDefDirectAssignmentContext) => {
    const partialRewrite: Userset = {
      this: {},
    };
    this.currentRelation?.rewrites?.push(partialRewrite);
  };
  exitRelationDefTypeRestriction = (ctx: RelationDefTypeRestrictionContext) => {
    const relationRef: Partial<RelationReference> = {};
    const baseRestriction = ctx.relationDefTypeRestrictionBase();
    if (!baseRestriction) {
      return;
    }

    relationRef.type = baseRestriction._relationDefTypeRestrictionType?.text;
    const usersetRestriction = baseRestriction._relationDefTypeRestrictionRelation;
    const wildcardRestriction = baseRestriction._relationDefTypeRestrictionWildcard;

    if (ctx.conditionName()) {
      relationRef.condition = ctx.conditionName().getText();
    }

    if (usersetRestriction) {
      relationRef.relation = usersetRestriction.text;
    }

    if (wildcardRestriction) {
      relationRef.wildcard = {};
    }

    this.currentRelation!.typeInfo!.directly_related_user_types!.push(relationRef as RelationReference);
  };

  exitRelationDefRewrite = (ctx: RelationDefRewriteContext) => {
    let partialRewrite: Userset = {
      computedUserset: {
        relation: ctx._rewriteComputedusersetName.text,
      },
    };

    if (ctx._rewriteTuplesetName) {
      partialRewrite = {
        tupleToUserset: {
          ...(partialRewrite as { computedUserset: ObjectRelation }),
          tupleset: {
            relation: ctx._rewriteTuplesetName.text,
          },
        },
      };
    }

    this.currentRelation?.rewrites?.push(partialRewrite);
  };

  enterRelationDefPartials = (ctx: RelationDefPartialsContext) => {
    if (ctx.OR_list().length) {
      this.currentRelation!.operator = RelationDefinitionOperator.RELATION_DEFINITION_OPERATOR_OR;
    } else if (ctx.AND_list().length) {
      this.currentRelation!.operator = RelationDefinitionOperator.RELATION_DEFINITION_OPERATOR_AND;
    } else if (ctx.BUT_NOT_list().length) {
      this.currentRelation!.operator = RelationDefinitionOperator.RELATION_DEFINITION_OPERATOR_BUT_NOT;
    }
  };

  enterCondition = (ctx: ConditionContext) => {
    if (ctx.conditionName() === null) {
      return;
    }
    if (!this.authorizationModel.conditions) {
      this.authorizationModel.conditions = {};
    }

    const conditionName = ctx.conditionName().getText();
    if (this.authorizationModel.conditions![conditionName]) {
      ctx.parser?.notifyErrorListeners(
        `condition '${conditionName}' is already defined in the model`,
        ctx.conditionName().start,
        undefined,
      );
    }

    this.currentCondition = {
      name: conditionName,
      expression: "",
      parameters: {},
    };
  };

  exitConditionParameter = (ctx: ConditionParameterContext) => {
    if (!ctx.parameterName() || !ctx.parameterType()) {
      return;
    }

    const parameterName = ctx.parameterName().getText();
    if (this.currentCondition?.parameters?.[parameterName]) {
      ctx.parser?.notifyErrorListeners(
        `parameter '${parameterName}' is already defined in the condition '${this.currentCondition?.name}'`,
        ctx.parameterName().start,
        undefined,
      );
    }

    const paramContainer = ctx.parameterType().CONDITION_PARAM_CONTAINER();
    const conditionParamTypeRef: Partial<ConditionParamTypeRef> = {};
    if (paramContainer) {
      conditionParamTypeRef.type_name = `TYPE_NAME_${paramContainer.getText().toUpperCase()}` as TypeName;
      const genericTypeName =
        ctx.parameterType().CONDITION_PARAM_TYPE() &&
        (`TYPE_NAME_${ctx.parameterType().CONDITION_PARAM_TYPE().getText().toUpperCase()}` as TypeName);
      if (genericTypeName) {
        conditionParamTypeRef.generic_types = [{ type_name: genericTypeName }];
      }
    } else {
      conditionParamTypeRef.type_name = `TYPE_NAME_${ctx.parameterType().getText().toUpperCase()}` as TypeName;
    }

    this.currentCondition!.parameters![parameterName] = conditionParamTypeRef as ConditionParamTypeRef;
  };

  exitConditionExpression = (ctx: ConditionExpressionContext) => {
    this.currentCondition!.expression = ctx.getText().trim();
  };

  exitCondition = (_ctx: ConditionContext) => {
    if (this.currentCondition) {
      this.authorizationModel.conditions![this.currentCondition.name!] = this.currentCondition!;

      this.currentCondition = undefined;
    }
  };
}

class OpenFgaDslErrorListener<T> extends ErrorListener<T> {
  errors: DSLSyntaxSingleError[] = [];

  syntaxError(
    _recognizer: Recognizer<T>,
    offendingSymbol: T,
    line: number,
    column: number,
    msg: string,
    e: RecognitionException | undefined,
  ) {
    let metadata = undefined;
    let columnOffset = 0;

    if (offendingSymbol instanceof antlr.Token) {
      metadata = {
        symbol: offendingSymbol.text,
      };
      columnOffset = metadata.symbol.length;
    }

    this.errors.push(
      new DSLSyntaxSingleError(
        {
          line: { start: line, end: line },
          column: { start: column, end: column + columnOffset },
          msg,
        },
        metadata,
        e,
      ),
    );
  }
}

export function parseDSL(data: string): {
  listener: OpenFgaDslListener;
  errorListener: OpenFgaDslErrorListener<unknown>;
} {
  const cleanedData = data
    .split("\n")
    .map((line) => line.trimEnd())
    .join("\n");

  const is = new antlr.InputStream(cleanedData);

  const errorListener = new OpenFgaDslErrorListener();

  // Create the Lexer
  const lexer = new OpenFGALexer(is as antlr.CharStream);
  lexer.removeErrorListeners();
  lexer.addErrorListener(errorListener);
  const stream = new antlr.CommonTokenStream(lexer);

  // Create the Parser
  const parser = new OpenFGAParser(stream);
  parser.removeErrorListeners();
  parser.addErrorListener(errorListener);

  // Finally parse the expression
  const listener = new OpenFgaDslListener();
  new antlr.ParseTreeWalker().walk(listener, parser.main());

  return { listener, errorListener };
}

/**
 * transformDSLToJSONObject - Converts models authored in FGA DSL syntax to the json syntax accepted by the OpenFGA API
 * @param {string} data
 * @returns {AuthorizationModel}
 */
export function transformDSLToJSONObject(data: string): AuthorizationModel {
  const { listener, errorListener } = parseDSL(data);

  if (errorListener.errors.length) {
    throw new DSLSyntaxError(errorListener.errors);
  }

  return listener.authorizationModel as AuthorizationModel;
}

/**
 * transformDSLToJSONObject - Converts models authored in FGA DSL syntax to a stringified json representation
 * @param {string} data
 * @returns {string}
 */
export function transformDSLToJSON(data: string): string {
  return JSON.stringify(transformDSLToJSONObject(data));
}
