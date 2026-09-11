package set

import (
	"iter"
	"maps"
	"math/bits"
	"slices"
)

// Set es un conjunto de elementos comparables sin orden. La memoria del
// mapa interno se gestiona con dos reglas de compactación (needsRehash y
// hintOversized, abajo del todo) para que las eliminaciones no dejen
// memoria sobre-reservada, sin tocar el camino normal de crecimiento.
//
// El valor cero (var s Set[T]) es utilizable: el mapa se crea con el
// primer Add.
type Set[T comparable] struct {
	set      map[T]struct{}
	capacity int
}

func New[T comparable](capacity int) *Set[T] {
	capacity = max(capacity, 0)
	return &Set[T]{
		set:      make(map[T]struct{}, capacity),
		capacity: capacity,
	}
}

func (s *Set[T]) estimatedSlotsLeft() int {
	setLen := s.Len()
	maxLenCap := max(setLen, s.capacity)
	return theoreticalSlots(maxLenCap) - setLen
}

// NewFromSlices crea un set con los elementos distintos de todos los
// slices. Reserva para la suma de longitudes; si los duplicados dejan el
// resultado por debajo de ese escalón, compacta.
func NewFromSlices[T comparable](slices ...[]T) *Set[T] {
	total := 0
	for _, s := range slices {
		total += len(s)
	}
	res := New[T](total)
	for _, s := range slices {
		for _, v := range s {
			res.set[v] = struct{}{}
		}
	}
	if hintOversized(total, res.Len()) {
		res.Rehash()
	}
	return res
}

func (s *Set[T]) Len() int {
	return len(s.set)
}

func (s *Set[T]) Add(e ...T) {
	if s.set == nil {
		s.set = make(map[T]struct{}, len(e))
	}
	makeNew := false
	var totalLen int
	if estimatedSlots := s.estimatedSlotsLeft(); estimatedSlots < len(e) {
		totalLen = s.Len() + len(e)
		makeNew = 2*(estimatedSlots+s.Len()) < theoreticalSlots(totalLen)
	}
	if makeNew {
		newSet := New[T](totalLen)
		maps.Copy(newSet.set, s.set)
		s.set = newSet.set
		s.capacity = newSet.capacity
	}
	s.AddSeq(slices.Values(e))
	if makeNew && hintOversized(totalLen, s.Len()) {
		s.Rehash()
	}
}

func (s *Set[T]) AddSeq(it iter.Seq[T]) {
	if s.set == nil {
		s.set = make(map[T]struct{})
	}
	for value := range it {
		s.set[value] = struct{}{}
	}
}

// Extend añade en sitio los elementos de sets.
func (s *Set[T]) Extend(sets ...*Set[T]) {
	extLen := 0
	for _, set := range sets {
		extLen += set.Len()
	}
	makeNew := false
	var totalLen int
	if estimatedSlotsLeft := s.estimatedSlotsLeft(); estimatedSlotsLeft < extLen {
		totalLen = s.Len() + extLen
		makeNew = 2*(estimatedSlotsLeft+s.Len()) < theoreticalSlots(totalLen)
	}
	if makeNew {
		newSet := New[T](totalLen)
		maps.Copy(newSet.set, s.set)
		s.set = newSet.set
		s.capacity = newSet.capacity

	}
	for _, set := range sets {
		maps.Copy(s.set, set.set)
	}
	if makeNew && hintOversized(totalLen, s.Len()) {
		s.Rehash()
	}
}

// Intersects deja s con la intersección de s y sets. Ojo al nombre: es
// un método mutante, no una consulta.
func (s *Set[T]) Intersects(sets ...*Set[T]) {
	if len(sets) == 0 {
		return
	}
	// Slice propio: append sobre el slice del llamador podría escribir
	// en su backing array.
	allSets := make([]*Set[T], 0, len(sets)+1)
	allSets = append(allSets, sets...)
	allSets = append(allSets, s)
	s.set = Intersection(allSets...).set
}

// Difference devuelve s − set.
func (s *Set[T]) Difference(set *Set[T]) *Set[T] {
	if s.Len() < set.Len() {
		// set es el más grande: recorrer s y quedarse con lo que no
		// está en set.
		res := New[T](s.Len())
		for value := range s.set {
			if !set.Contains(value) {
				res.set[value] = struct{}{}
			}
		}
		if hintOversized(s.Len(), res.Len()) {
			res.Rehash()
		}
		return res
	}
	res := s.Copy()
	res.Subtract(set)
	return res
}

// Subtract elimina en sitio los elementos de sets.
func (s *Set[T]) Subtract(sets ...*Set[T]) {
	before := s.Len()
	for _, set := range sets {
		for value := range set.IterAll() {
			delete(s.set, value)
		}
	}
	if needsRehash(before, s.Len()) {
		s.Rehash()
	}
}

// Remove elimina los elementos dados. Eliminar elementos ausentes es un
// no-op y nunca dispara un rehash.
func (s *Set[T]) Remove(e ...T) {
	before := s.Len()
	for _, value := range e {
		delete(s.set, value)
	}
	if needsRehash(before, s.Len()) {
		s.Rehash()
	}
}

// Rehash reconstruye el mapa interno con capacidad exacta para su
// longitud: re-hashea cada clave, elimina tombstones y aterriza en el
// escalón mínimo.
//
// Deliberadamente con maps.Copy y no con maps.Clone: Clone del runtime
// replica la estructura interna tal cual (mismos slots y mismos
// tombstones), o sea que conservaría justo la sobre-asignación que aquí
// se quiere eliminar.
func (s *Set[T]) Rehash() {
	cloned := New[T](s.Len())
	maps.Copy(cloned.set, s.set)
	s.set = cloned.set
	s.capacity = cloned.capacity
}

// Clone devuelve una copia independiente y compacta: capacidad exacta
// para su longitud, sin heredar el exceso del original. A diferencia de
// maps.Clone, nunca devuelve un mapa a nil.
func (s *Set[T]) Clone() *Set[T] {
	newSet := New[T](0)
	newSet.set = maps.Clone(s.set)
	newSet.capacity = s.capacity
	return newSet
}

func (s *Set[T]) Copy() *Set[T] {
	newSet := New[T](s.Len())
	maps.Copy(newSet.set, s.set)
	return newSet
}

func (s *Set[T]) Contains(e T) bool {
	_, res := s.set[e]
	return res
}

func (s *Set[T]) GetAll() []T {
	res := make([]T, s.Len())
	i := 0
	for key := range s.IterAll() {
		res[i] = key
		i++
	}
	return res
}

func (s *Set[T]) IterAll() iter.Seq[T] {
	return maps.Keys(s.set)
}

func (s *Set[T]) IsSubset(set *Set[T]) bool {
	if set.Len() < s.Len() {
		return false
	}
	for value := range s.IterAll() {
		if !set.Contains(value) {
			return false
		}
	}
	return true
}

func (s *Set[T]) Disjoint(set *Set[T]) bool {
	small, large := s, set
	if large.Len() < small.Len() {
		small, large = large, small
	}
	for value := range small.IterAll() {
		if large.Contains(value) {
			return false
		}
	}
	return true
}

func (s *Set[T]) Equal(set *Set[T]) bool {
	if s.Len() != set.Len() {
		return false
	}
	return s.IsSubset(set)
}

func Union[T comparable](sets ...*Set[T]) *Set[T] {
	newSet := New[T](0)
	newSet.Extend(sets...)
	return newSet
}

func Intersection[T comparable](sets ...*Set[T]) *Set[T] {
	if len(sets) == 0 {
		return New[T](0)
	}
	if len(sets) == 1 {
		return sets[0].Copy()
	}
	minSet := sets[0]
	for _, set := range sets[1:] {
		if set.Len() < minSet.Len() {
			minSet = set
		}
	}
	if minSet.Len() == 0 {
		return New[T](0)
	}
	// Recorrer el más pequeño y comprobar pertenencia en los demás:
	// menos consultas que al revés.
	newSet := New[T](minSet.Len())
	for value := range minSet.IterAll() {
		inAll := true
		for _, set := range sets {
			if set == minSet {
				continue // la clave ya salió de minSet
			}
			if !set.Contains(value) {
				inAll = false
				break
			}
		}
		if inAll {
			newSet.set[value] = struct{}{}
		}
	}
	if hintOversized(minSet.Len(), newSet.Len()) {
		newSet.Rehash()
	}
	return newSet
}

// theoreticalSlots devuelve los slots que make(map, hint) reserva en el
// momento de crearlo. Modelo del runtime (Go 1.24+, Swiss maps):
// target = hint*8/7, directorio de ceil(target/1024) entradas (redondeado
// a potencia de 2), tablas de target/dirSize slots (redondeado arriba).
// El redondeo puede dejar el presupuesto (7/8 de los slots) por debajo de
// hint: son los "cracks" que needsRehash detecta.
func theoreticalSlots(hint int) int {
	if hint <= 0 {
		return 0
	}
	if hint <= 8 {
		return 8 // small map: un grupo tras la primera inserción
	}
	target := hint * 8 / 7
	dirSize := pow2ceil((target + 1023) / 1024)
	table := pow2ceil(target / dirSize)
	table = max(table, 8)
	return dirSize * table
}

func pow2ceil(v int) int {
	if v <= 1 {
		return 1
	}
	return 1 << bits.Len(uint(v-1))
}

// needsRehash decide si conviene reconstruir un mapa CON HISTORIA
// (crecido orgánicamente o con borrados previos) que bajó de before a
// after elementos. Solo lo usan Remove y Subtract; para maps recién
// construidos ver hintOversized.
//
// Un mapa queda sobre-asignado en dos situaciones:
//
//  1. Cayó un escalón teórico (theoreticalSlots(before) !=
//     theoreticalSlots(after)): el mapa real nunca tiene menos slots que
//     el escalón de partida, así que si el escalón baja, sobra memoria
//     garantizada.
//
//  2. Venía justo por encima del tope de su escalón. El runtime reparte
//     las claves entre tablas con un hash aleatorio: cerca del tope
//     (7/8 de los slots) alguna tabla se pasa de su presupuesto, se
//     divide, y el mapa queda con más slots de los que make(after)
//     asignaría.
//
// Todas las condiciones son de CRUCE, no de zona: disparan solo al BAJAR
// de un umbral, no mientras se está por encima de él. Como las
// eliminaciones solo hacen decrecer len, cada umbral se cruza a lo sumo
// una vez por escalón: es imposible entrar en el bucle de rehashear una
// y otra vez sin ganar nada (con la regla anterior, de zona, se midieron
// 116 rehashes seguidos —61 inútiles— borrando 100k→50k de uno en uno).
//
// El umbral de la condición 2 no es el tope 7T/8 sino X = 4T/5
// (carga 0,80). Reconstruir con make(len) deja el mapa a carga len/T, y
// el reparto multinomial del hash entre tablas puede pasarse del
// presupuesto por tabla (896 de 1024) y dividir alguna tabla: a carga
// 0,875 (la banda) la probabilidad es ~1 en cualquier T. El margen de
// X son ~2,7σ por tabla, y ese margen es aproximadamente constante en
// T (verificado de 2k a 1M: el exceso esperado sigue Phi(-z)·T sin
// tendencia con T), así que la constante no necesita depender de T.
// Con X = 7T/8 − T/10 (0,775T) el margen era ~3,7σ: más margen del
// necesario en T grandes, a costa de cazar el residuo más tarde.
// El falso positivo (map limpio que cruza X sin necesitarlo) no se
// puede evitar sin unsafe: es el coste asumido de la regla.
func needsRehash(before, after int) bool {
	before, after = max(before, after), min(before, after)
	if before == after {
		return false // no se eliminó nada (p. ej. Remove de ausentes)
	}
	t1, t2 := theoreticalSlots(before), theoreticalSlots(after)
	if t1 != t2 {
		return true // condición 1: cayó un escalón
	}
	if before <= 8 {
		return false // small map: 8 slots es el suelo
	}
	if t1 >= 2048 {
		// multi-tabla: umbral X
		x := 4 * t1 / 5
		return before >= x && after < x
	}
	// T <= 1024: umbral = tope 7T/8. Aquí el mapa nuevo aterriza limpio
	// en el propio tope (tabla única o presupuesto exacto), sin margen
	// de varianza que justifique esperar más.
	band := 7 * t1 / 8
	return before > band && after <= band
}

// hintOversized decide si un mapa RECIÉN CONSTRUIDO con make(hint) y
// llenado hasta actual (<= hint) quedó por debajo del escalón reservado.
// Solo eso importa: un mapa recién hecho a carga <= 7/8 ya está en su
// tamaño natural, y rehashearlo solo vuelve a repartir los mismos hashes
// (mismo resultado esperado, mismo coste O(n)). La regla de historia
// (needsRehash) no debe aplicarse aquí: sus umbrales de cruce dispararían
// rebuilds inútiles sobre maps limpios.
func hintOversized(hint, actual int) bool {
	return theoreticalSlots(hint) != theoreticalSlots(actual)
}
