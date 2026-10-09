/** The keyboard half of the header search, kept free of React so it can
 *  be tested: which option is active after a key, and what Enter opens.
 *
 *  `active` is -1 while focus is only in the text box, which is the
 *  state typing leaves it in; Enter then opens the first hit, as it
 *  always has. */
export function nextActive(active: number, key: string, count: number): number {
  if (count <= 0) return -1;
  switch (key) {
    case "ArrowDown":
      return active < 0 || active >= count - 1 ? 0 : active + 1;
    case "ArrowUp":
      return active <= 0 ? count - 1 : active - 1;
    case "Home":
      return 0;
    case "End":
      return count - 1;
    default:
      return active;
  }
}

/** The option Enter opens: the one the arrows reached, else the first. */
export function chosenIndex(active: number, count: number): number {
  if (count <= 0) return -1;
  return active >= 0 && active < count ? active : 0;
}
