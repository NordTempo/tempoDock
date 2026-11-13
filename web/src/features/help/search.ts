import { type Do, type TopicId, topicIds, topics } from "./topics";

export type TopicMatch = {
  id: TopicId;
  title: string;
  rows: Do[];
  score: number;
};

/**
 * Topics matching every word of `query` (in their title, line, search
 * words or rows), best first: a word in the title counts most, then at a
 * word's start. Each match carries its rows that hold a word, so a
 * search for "memory limit" shows that row, not the whole topic.
 * Operator-only topics count only for operators.
 */
export function matchTopics(query: string, operator: boolean): TopicMatch[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return [];
  const found: TopicMatch[] = [];
  for (const id of topicIds) {
    const topic = topics[id];
    if (topic.operator && !operator) continue;
    const rowText = (row: Do) => `${row.what} ${row.cli ?? ""}`.toLowerCase();
    const text = [
      topic.title,
      topic.what,
      topic.words ?? "",
      ...topic.do.map(rowText),
    ]
      .join(" ")
      .toLowerCase();
    let score = 0;
    let all = true;
    for (const word of words) {
      const at = text.indexOf(word);
      if (at < 0) {
        all = false;
        break;
      }
      if (topic.title.toLowerCase().includes(word)) score += 5;
      if (at === 0 || /[\s/.-]/.test(text[at - 1] ?? "")) score += 2;
    }
    if (!all) continue;
    const rows = topic.do.filter((row) =>
      words.some((w) => rowText(row).includes(w)),
    );
    found.push({ id, title: topic.title, rows, score });
  }
  return found.sort((a, b) => b.score - a.score);
}
