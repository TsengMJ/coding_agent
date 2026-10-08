import { useState } from "react";
import type { Answer, Plan, Question, Todo } from "../types";
import { todoMark, type PlanDraft } from "../model/plan";
import { CopyButton, Markdown, Section } from "./common";

// TodoList is a checklist with each item's notes under it.
export function TodoList({ todos }: { todos: Todo[] }) {
  return (
    <ul className="todos">
      {todos.map((t) => (
        <li key={t.id} className={t.status === "completed" ? "done" : t.status}>
          <span>{todoMark(t.status)}</span>
          <span className="todo-text">
            {t.content}
            {t.notes && <span className="todo-notes">{t.notes}</span>}
          </span>
        </li>
      ))}
    </ul>
  );
}

export function doneCount(todos: Todo[]): string {
  return `${todos.filter((t) => t.status === "completed").length}/${todos.length} done`;
}

// PlanDoc renders a plan for review: overview, markdown body and checklist.
export function PlanDoc({ plan, id }: { plan: PlanDraft; id?: string }) {
  return (
    <>
      <Section
        title={`Plan · ${plan.name}`}
        extra={id ? <span className="muted mono" style={{ fontSize: 11.5 }}>{id} <CopyButton text={id} label="⧉" /></span> : undefined}
      >
        {plan.overview && <div className="plan-overview">{plan.overview}</div>}
        <Markdown text={plan.body} />
      </Section>
      <Section title="Todos" extra={<span className="badge">{plan.todos.length} steps</span>}>
        <TodoList todos={plan.todos} />
      </Section>
    </>
  );
}

// PlanBanner offers to build the thread's draft plan.
export function PlanBanner({ plan, onBuild, busy }: { plan: Plan; onBuild: () => void; busy: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="plan-banner">
      <div className="plan-banner-head">
        <span className="cat-plan">Plan ready</span>
        <span className="plan-banner-name">{plan.name}</span>
        <span className="muted">{plan.todos.length} todos</span>
        <span style={{ flex: 1 }} />
        <button className="btn" onClick={() => setOpen(!open)}>
          {open ? "Hide plan" : "Show plan"}
        </button>
        <button className="btn primary" disabled={busy} onClick={onBuild} title="Leave plan mode and implement this plan">
          Build
        </button>
      </div>
      {open && (
        <div className="plan-banner-body">
          <PlanDoc plan={plan} id={plan.id} />
        </div>
      )}
    </div>
  );
}

// AskForm answers the questions of one ask_user call.
export function AskForm({
  questions,
  answers,
  onChange,
}: {
  questions: Question[];
  answers: Record<string, Answer>;
  onChange: (a: Answer) => void;
}) {
  return (
    <div className="ask-form">
      {questions.map((q, qi) => {
        const a = answers[q.id] ?? { question_id: q.id, option_ids: [] };
        const toggle = (id: string) => {
          const ids = q.allow_multiple
            ? a.option_ids.includes(id)
              ? a.option_ids.filter((x) => x !== id)
              : [...a.option_ids, id]
            : [id];
          onChange({ ...a, option_ids: ids });
        };
        return (
          <fieldset key={q.id} className="ask-question">
            <legend>
              {qi + 1}. {q.prompt}
              {q.allow_multiple && <span className="muted"> (choose any)</span>}
            </legend>
            {q.options.map((o) => (
              <label key={o.id} className="ask-option">
                <input
                  type={q.allow_multiple ? "checkbox" : "radio"}
                  name={`ask-${q.id}`}
                  checked={a.option_ids.includes(o.id)}
                  onChange={() => toggle(o.id)}
                />
                {o.label}
              </label>
            ))}
            <input
              className="ask-other"
              placeholder="Other (type your own answer)"
              value={a.other ?? ""}
              onChange={(e) => onChange({ ...a, other: e.target.value })}
            />
          </fieldset>
        );
      })}
    </div>
  );
}
