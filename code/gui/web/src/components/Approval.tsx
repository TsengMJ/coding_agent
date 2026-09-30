import { useState } from "react";
import type { ActionRequest, Decision, Interrupt } from "../types";
import { countKinds } from "../model/diff";
import { str } from "../model/messages";
import { summarize } from "../model/tools";
import { Command } from "./common";
import { DiffView } from "./DiffView";
import { JsonTree } from "./JsonTree";
import { requestedChange } from "./ToolDetail";

// ApprovalCard asks for a decision on every tool call awaiting review and
// sends them together, in order.
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
  if (requests.length === 0) return null;
  const all = (d: Decision) => onDecide(requests.map(() => d));
  return (
    <div className="approval">
      <div className="approval-head">
        {requests.length === 1 ? "1 tool call needs" : `${requests.length} tool calls need`} your approval
      </div>
      {requests.map((r, i) => (
        <div className="approval-item" key={i}>
          <Preview req={r} />
        </div>
      ))}
      <div className="approval-actions">
        <button className="btn primary" disabled={busy} onClick={() => all({ type: "approve" })} autoFocus>
          Approve{requests.length > 1 ? " all" : ""}
        </button>
        <input
          className="reason"
          placeholder="Reason for rejecting (sent to the model)"
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && reason.trim()) all({ type: "reject", message: reason.trim() });
          }}
        />
        <button className="btn danger" disabled={busy} onClick={() => all({ type: "reject", message: reason.trim() || undefined })}>
          Reject{requests.length > 1 ? " all" : ""}
        </button>
      </div>
    </div>
  );
}

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
      <JsonTree value={req.args} />
    </>
  );
}
