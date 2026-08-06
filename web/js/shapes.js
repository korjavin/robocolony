// The robot silhouettes, in one place.
//
// A robot's shape is its chassis and a barrel past the nose means a weapon
// (design §7.1); the arena, the match legend and the blueprint configurator all
// draw from these strings, so a design's preview is literally the shape the
// arena will draw. They lived in match.js until the configurator needed them —
// two copies of a silhouette is two robots that stop looking alike the day one
// is retuned.
//
// The paths are unit space, centred on the origin, spanning about ±1, and drawn
// pointing north — heading 0 — so one rotate() orients a robot and the shape's
// nose *is* the heading mark. That replaces a separate notch: forward vision is
// the core mechanic, and a shape whose front you can see says it without
// spending a second mark on it.
//
// Keyed by the slug of the locomotion component's catalogue name, so a chassis
// added server-side either finds its silhouette here or visibly falls back to
// the plain body — which is the signal to draw it one.
//
// Armour tier, weapon *count*, radar and manipulator are deliberately not
// encoded. A cell is about 14px at the default arena size; an outline weight, a
// second barrel or a dish are invisible there, and a silhouette that tries to
// say six things says none.
export const SHAPES = {
  // Tracks: flat sides, square tail, blunt mass. The workhorse reads as a hull.
  tracks: "M0-1.25L.9-.5L.9.9L-.9.9L-.9-.5Z",
  // Legs: the same body carried on splayed limbs. The spurs are the silhouette.
  legs: "M0-1.15L.5-.55L1.15-.45L.6.05L1 1.15L.3.45L-.3.45L-1 1.15L-.6.05L-1.15-.45L-.5-.55Z",
  // Anti-gravity: a smooth lens with nothing angular on it and no ground contact.
  "anti-gravity-platform": "M0-1.25C.85-.8.95.3 0 .95C-.95.3-.85-.8 0-1.25Z",
  // A locomotion the catalogue has grown and this file has never heard of, or a
  // robot whose blueprint is not on this frame: a plain body, still with a nose,
  // so an unknown chassis never costs the player the heading.
  unknown: "M0-1.3L.72-.7A1 1 0 1 1-.72-.7Z",
};

// A barrel past the nose, in the same unit space and in the weapon colour the
// loose components and the legend already use. Binary: one weapon or two draws
// the same barrel, because two barrels 2px apart is noise, not information.
export const MUZZLE = "M-.26-.9L.26-.9L.26-1.8L-.26-1.8Z";

// slug turns a catalogue name into a SHAPES key. Shared for the same reason the
// paths are: the lookup and the table have to agree.
export const slug = (s) => String(s).toLowerCase().replace(/[^a-z0-9]+/g, "-");
