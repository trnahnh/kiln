import { source } from "@/content/readouts";

interface Props {
  n: number;
  title: string;
  drawnBy: string;
}

export const SHEET_COUNT = 6;

export default function TitleBlock({ n, title, drawnBy }: Props) {
  return (
    <div className="title-block" aria-label={`Sheet ${n} of ${SHEET_COUNT}, ${title}`}>
      <div className="tb-cell">
        <span className="tb-k">Project</span>
        <span className="tb-v">KILN</span>
      </div>
      <div className="tb-cell">
        <span className="tb-k">Title</span>
        <span className="tb-v">{title}</span>
      </div>
      <div className="tb-cell">
        <span className="tb-k">Sheet</span>
        <span className="tb-v">
          {n} OF {SHEET_COUNT}
        </span>
      </div>
      <div className="tb-cell">
        <span className="tb-k">Drawn by</span>
        <span className="tb-v">{drawnBy}</span>
      </div>
      <div className="tb-cell">
        <span className="tb-k">Checked / rev</span>
        <a href={source.runUrl} className="tb-v accent">
          RUN {source.runId} / {source.commit}
        </a>
      </div>
    </div>
  );
}
