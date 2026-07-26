package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// LuminaTask persists the ownership and upstream binding of a public ModelArk task.
type LuminaTask struct {
	ent.Schema
}

func (LuminaTask) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "lumina_tasks"},
	}
}

func (LuminaTask) Fields() []ent.Field {
	return []ent.Field{
		field.String("task_id").MaxLen(64).Immutable(),
		field.Int64("user_id"),
		field.Int64("api_key_id"),
		field.Int64("group_id"),
		field.Int64("account_id"),
		field.String("upstream_task_id").MaxLen(128),
		field.String("task_type").MaxLen(32),
		field.String("model").MaxLen(128),
		field.String("status").MaxLen(32).Default("queued"),
		field.JSON("request_payload", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.JSON("response_payload", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.String("error_code").Optional().Nillable().MaxLen(128),
		field.String("error_message").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("completed_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("user_deleted_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (LuminaTask) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("task_id").Unique(),
		index.Fields("user_id", "api_key_id", "created_at"),
		index.Fields("account_id", "upstream_task_id").Unique(),
		index.Fields("status"),
		index.Fields("user_deleted_at"),
	}
}
