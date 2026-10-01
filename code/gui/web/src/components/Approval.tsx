import { useState } from "react";
import type { ActionRequest, Answer, Decision, Interrupt } from "../types";
import { countKinds } from "../model/diff";
import { str } from "../model/messages";
import { ASK_TOOL, formatAnswers, questionsOf } from "../model/plan";
import { summarize } from "../model/tools";
import { Command } from "./common";
import { AskForm } from "./Plan";
import { DiffView } from "./DiffView";
import { JsonTree } from "./JsonTree";
import { PatchPreview, requestedChange } from "./ToolDetail";

// ApprovalCard asks for a decision on every tool call awaiting review and
// sends them together, in order. ask_user calls show their questions and
// are answered (respond) or skipped (reject) instead.
export function ApprovalCard({
  interrupts,
  onDecide,
  busy,
}: {
  interrupts: Interrupt[];
  onDecide: (d: Decision[]) => void;
  busy: boolean;
}) {
  const requests = interrupts.flatMap((i) => i.action_requests);
  const [reason, setReason] = useState("");
  // answers holds ask_user answers by request index, then question id.
  const [answers, setAnswers] = useState<Record<number, Record<string, Answer>>>({});
  if (requests.length === 0) return null;
  const questions = requests.map((r) => (r.name === ASK_TOOL ? questionsOf(r.args) : null));
  const asks = questions.filter(Boolean).length;
  const approvals = requests.length - asks;
  const decide = (approve: boolean) =>
    onDecide(
      requests.map((_, i): Decision => {
        const qs = questions[i];
        if (qs && approve) {
          return { type: "respond", message: formatAnswers(qs, Object.values(answers[i] ?? {})) };
        }
        if (qs) return { type: "reject", message: SKIPPED };
        return approve ? { type: "approve" } : { type: "reject", message: reason.trim() || undefined };
      }),
    );
  const head =
    asks > 0 && approvals === 0
      ? "The agent has questions for you"
      : `${approvals === 1 ? "1 tool call needs" : `${approvals} tool calls need`} your approval${asks ? " · the agent also has questions" : ""}`;
  return (
    <div className="approval">
      <div className="approval-head">{head}</div>
      {requests.map((r, i) => (
        <div className="approval-item" key={i}>
          {questions[i] ? (
            <AskForm
              questions={questions[i]!}
              answers={answers[i] ?? {}}
              onChange={(a) => setAnswers((old) => ({ ...old, [i]: { ...old[i], [a.question_id]: a } }))}
            />
          ) : (
            <Preview req={r} />
          )}
        </div>
      ))}
      <div className="approval-actions">
        <button className="btn primary" disabled={busy} onClick={() => decide(true)} autoFocus>
          {approvals === 0 ? "Send answers" : asks ? "Approve and answer" : `Approve${requests.length > 1 ? " all" : ""}`}
        </button>
        {approvals > 0 && (
          <input
            className="reason"
            placeholder="Reason for rejecting (sent to the model)"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && reason.trim()) decide(false);
            }}
          />
        )}
        <button className="btn danger" disabled={busy} onClick={() => decide(false)}>
          {approvals === 0 ? "Skip" : `Reject${requests.length > 1 ? " all" : ""}`}
        </button>
      </div>
    </div>
  );
}

const SKIPPED = "The user skipped these questions; proceed with your best judgment and state your assumptions.";

function Preview({ req }: { req: ActionRequest }) {
  const call = { id: "", name: req.name, args: req.args };
  const head = (
    <div style={{ marginBottom: 6 }}>
      <span className="tool-name cat-edit">{req.name}</span> <span className="mono">{summarize(call)}</span>
    </div>
  );
  if (req.name === "execute") {
    return (
      <>
        {head}
        <Command text={str(req.args["command"])} />
      </>
    );
  }
  if (req.name === "delete") {
    return (
      <>
        {head}
        <div className="mono">Deletes {str(req.args["file_path"])}; a directory is removed with everything inside it.</div>
      </>
    );
  }
  if (req.name === "apply_patch") {
    return (
      <>
        {head}
        <PatchPreview patch={str(req.args["patch"])} label="proposed" />
      </>
    );
  }
  const hunk = requestedChange({ call });
  if (hunk) {
    return (
      <>
        {head}
        <DiffView hunks={[hunk]} path={str(req.args["file_path"])} {...countKinds(hunk.lines)} label="proposed" />
      </>
    );
  }
  return (
    <>
      {head}
      {req.description && (
        <div className="mono" style={{ whiteSpace: "pre-wrap", marginBottom: 6 }}>
          {req.description}
        </div>
      )}
      <JsonTree value={req.args} />
    </>
  );
}
