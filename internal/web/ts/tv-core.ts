// What the TV pages share and a test can run without a browser.

/** The address a person opens on their phone: the TV page's address without /tv or /tv/legacy. */
export function phoneAddress(loc: { host: string; pathname: string }): string {
  return loc.host + loc.pathname.replace(/\/tv(\/legacy)?\/?$/, "/");
}
