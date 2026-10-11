/** Who sees the effective-access panels. The console's Explain refuses
 *  everyone but an operator (and the roster controllers, which are not
 *  people), so a panel shown to anyone else would only show a refusal. */
export function explainPanels(operator: boolean): { person: boolean; simulator: boolean } {
  return { person: operator, simulator: operator };
}
