# Laboratorio 3 — Algoritmos de Enrutamiento

**Universidad del Valle de Guatemala**
**CC3067 — Redes**
**Fecha de entrega:** 3 de septiembre de 2026

**Integrantes del grupo:**

| Nombre | Carné |
| --- | --- |
| _Iris Ayala_ | _23965_ |
| _Jonatan Díaz_ | _23837_ |
| _Luis Padilla_ | _2366_ |
| _Anggie Quezada_ | _23643_ |

**Repositorio:** _(completar con el enlace)_

---

## 1. Descripción de la práctica

El laboratorio consiste en construir una red simulada en la que cada nodo es un
proceso independiente que se comunica con sus vecinos mediante sockets TCP.
Cada nodo conoce inicialmente solo a sus vecinos directos y debe construir su
tabla de enrutamiento para poder entregar mensajes a destinos con los que no
tiene un enlace directo.

Se implementaron tres algoritmos intercambiables sobre la misma infraestructura
de red:

1. **Dijkstra**, que calcula caminos mínimos a partir de una topología conocida.
2. **Flooding**, que reenvía cada paquete a todos sus vecinos.
3. **Link State Routing (LSR)**, que descubre la topología intercambiando
   anuncios de estado de enlace y luego aplica Dijkstra sobre ella.

La implementación se realizó en Go. La elección respondió a tres criterios: el
modelo de concurrencia mediante goroutines corresponde directamente a la
separación entre plano de forwarding y plano de ruteo que exige el enunciado; el
compilador produce un binario estático único, lo que resuelve la portabilidad
entre plataformas sin gestores de dependencias; y la biblioteca estándar cubre
sockets TCP y serialización JSON, por lo que el proyecto no tiene dependencias
externas.

---

## 2. Descripción de los algoritmos utilizados y su implementación

### 2.1 Arquitectura general

El enunciado señala que Dijkstra y Flooding se utilizan dentro de LSR y que por
ello se requiere alta modularidad. Este requisito determinó la arquitectura del
proyecto: ambos algoritmos se implementaron como **funciones puras**, sin
conocimiento alguno de sockets ni de paquetes en tránsito.

```go
func Dijkstra(g Graph, source string) Table
func Flood(neighbors []string, pkt *protocol.Packet) []string
```

De esta forma existe una sola implementación de cada algoritmo en todo el
proyecto. El modo `dijkstra` y el modo `flooding` invocan esas funciones
directamente, y LSR invoca las mismas: `Flood` para diseminar sus anuncios y
`Dijkstra` para derivar la tabla de forwarding a partir de la base de datos de
enlaces.

La organización del código es la siguiente:

```
cmd/node/            punto de entrada del proceso
internal/protocol/   envelope JSON y payload de estado de enlace
internal/transport/  servidor y cliente TCP
internal/config/     carga de topología y tabla de nombres
internal/routing/    plano de ruteo (grafo, flooding, algoritmos)
internal/node/       plano de forwarding y descubrimiento de vecinos
internal/cli/        consola interactiva
```

Cada algoritmo implementa la interfaz `Algorithm`, que aísla las decisiones de
ruteo del transporte:

```go
type Algorithm interface {
    Proto() protocol.Proto
    Start(ctx context.Context)
    Forward(pkt *protocol.Packet) []string
    HandleInfo(pkt *protocol.Packet)
    OnLinkUp(neighbor string, cost float64)
    OnLinkDown(neighbor string)
    Table() Table
}
```

El detalle relevante es que `Forward` devuelve una lista de vecinos. Esa firma
unifica flooding, que reenvía a varios destinos, con Dijkstra y LSR, que
reenvían a uno solo, sin que el plano de forwarding necesite distinguir entre
algoritmos.

Recíprocamente, el nodo expone a los algoritmos únicamente la interfaz
`Fabric` (`ID`, `Neighbors`, `SendTo`, `Logf`). Un algoritmo no puede abrir
conexiones ni conocer direcciones: solo decide hacia qué vecino lógico va un
paquete.

### 2.2 Concurrencia: los dos planos

El enunciado exige que forwarding y routing corran en paralelo. Ambos se
implementaron como goroutines que se comunican por canales, no por memoria
compartida:

- **Plano de forwarding.** Las goroutines de conexión decodifican cada línea
  JSON y depositan el paquete en un canal (`inbox`). Una única goroutine drena
  ese canal y procesa los paquetes. Con esto, un recálculo de rutas nunca
  bloquea un socket, y el manejo de paquetes no requiere candados propios.
- **Plano de ruteo.** Una goroutine sondea periódicamente a los vecinos
  mediante paquetes `hello` y declara caído al que deja de responder. El
  algoritmo activo recibe eventos `OnLinkUp` / `OnLinkDown` en lugar de
  consultar el estado.

La tabla de enrutamiento se protege con `SafeTable`, que utiliza un
`sync.RWMutex`: el plano de ruteo la reemplaza completa, el de forwarding la
consulta por paquete.

### 2.3 Protocolo

Cada paquete es un objeto JSON terminado en salto de línea sobre TCP (NDJSON),
UTF-8, con un máximo de 65536 bytes por línea. El delimitado por línea mantiene
el framing trivial y permite inspeccionar un nodo con `nc`.

Este formato fue negociado entre todos los grupos antes de la prueba en clase
(sección 3.6), y difiere del primer borrador interno del proyecto en tres
puntos: los campos `from`/`to` se escriben como `IP:puerto` en vez del id corto
de cada equipo, se agregaron `version` y `checksum`, y tres encabezados
cambiaron de nombre. La traducción entre el id corto (`A`, `B`, ...) que usa
internamente cada nodo y la dirección que exige el protocolo ocurre solo en la
frontera de red (al armar y al leer un paquete), por lo que Dijkstra, Flooding
y LSR no tuvieron que modificarse.

```json
{
  "version": 1,
  "proto": "lsr",
  "type": "message",
  "from": "192.168.0.128:5000",
  "to": "192.168.0.59:5000",
  "ttl": 16,
  "headers": [{"msg_id": "3f2a..."}, {"checksum": "0bded535"}, {"trace": ["192.168.0.128:5000"]}],
  "payload": "hola"
}
```

Tipos de paquete implementados:

| Tipo | Función |
| --- | --- |
| `hello` | Sondea a un vecino, con payload `{"listen_port": 5000}`; el receptor responde con `echo`. |
| `echo` | Devuelve el `t0` original para medir el viaje de ida y vuelta. |
| `info` | Transporta un paquete de estado de enlace (LSP): `{"origin", "seq", "age_s", "neighbors": [{"id","weight"}]}`. |
| `message` | Datos de usuario; se reenvía o se imprime al llegar a destino. |

Encabezados propios. Los encabezados desconocidos provenientes de otras
implementaciones se preservan intactos al reenviar un paquete, lo cual es
necesario para la interoperabilidad entre grupos.

| Encabezado | Propósito |
| --- | --- |
| `msg_id` | Identificador único, para descartar duplicados; si falta, se deriva de forma determinística de `(from,to,type,payload)`. |
| `checksum` | CRC32 (hex, 8 dígitos) del payload canónico; una discrepancia se registra pero nunca descarta el paquete. |
| `via` | Dirección del salto anterior, para no devolver el paquete por donde llegó. |
| `t0` | Timestamp del emisor, en segundos Unix fraccionarios, para medir el costo del enlace. |
| `trace` | Traza de las direcciones recorridas. |

El decodificador tolera paquetes de otros grupos que omitan campos: un paquete
sin `ttl` recibe el valor por defecto, uno sin `msg_id` recibe uno derivado
determinísticamente, y un `version` ausente o distinto de 1 se registra pero
nunca es motivo para descartar el paquete. De igual forma, el payload de un LSP
se acepta en las variantes que otros equipos puedan enviar (vecinos como
diccionario, la clave `links` en vez de `neighbors`, o el payload completo
serializado como texto), aunque este nodo siempre emite la forma canónica.

### 2.4 Dijkstra

**Entrada:** la topología completa (nodos y aristas), leída de un archivo de
configuración.

Se implementó con una cola de prioridad (`container/heap`), lo que da una
complejidad de O((V + E) log V) frente a O(V²) de la versión con búsqueda
lineal del mínimo.

La particularidad respecto a la formulación de libro es que una tabla de
enrutamiento no necesita el camino completo, sino únicamente el **primer
salto**. Por eso, además de las distancias, el algoritmo propaga `firstHop`:
cuando se relaja una arista desde el nodo origen, el primer salto es el vecino
mismo; en cualquier otro caso se hereda el primer salto del nodo desde el que se
llegó.

Los vecinos se recorren en orden alfabético al relajar aristas. Esto no afecta
la corrección, pero hace que la elección entre caminos de igual costo sea
reproducible entre ejecuciones y entre nodos, lo que facilita interpretar las
trazas.

En modo `dijkstra` la tabla se calcula una sola vez al arrancar. Aun así, el
nodo reacciona a fallas: al detectar que un vecino dejó de responder, poda sus
enlaces de la topología y recalcula, de modo que no se convierte en un agujero
negro para el tráfico.

### 2.5 Flooding

**Entrada:** únicamente el conocimiento de sus vecinos.

Cada paquete de datos se reenvía a todos los vecinos excepto a aquel del que se
recibió. No se mantiene tabla de enrutamiento alguna.

El problema central del flooding es la terminación: en una topología con ciclos
—como la utilizada en el laboratorio— el número de copias crece
exponencialmente. Se implementaron tres mecanismos independientes, porque en la
prueba en clase la red incluye implementaciones de otros grupos y no se puede
depender de un solo mecanismo:

1. **Caché de `msg_id`** (`SeenCache`). Un paquete ya procesado se descarta. Las
   entradas expiran tras un tiempo, de modo que una ejecución larga no consume
   memoria sin límite. Este es el mecanismo que efectivamente corta los ciclos.
2. **Números de secuencia**, aplicables a los anuncios de estado de enlace: un
   LSP cuya secuencia no supera a la almacenada no se vuelve a difundir.
3. **TTL**, un presupuesto fijo de saltos, como último recurso frente a un nodo
   externo con comportamiento incorrecto.

Adicionalmente se aplica *split horizon*: nunca se reenvía por el enlace del que
se recibió el paquete. No es necesario para la corrección, ya que la caché de
duplicados basta, pero reduce a la mitad el tráfico por enlace.

### 2.6 Link State Routing

**Entrada:** sus vecinos directos, y los anuncios de los demás nodos, de los
cuales se deriva la topología.

El ciclo de operación es el siguiente:

1. **Descubrimiento.** El nodo envía `hello` a sus vecinos configurados. El
   receptor responde con `echo` devolviendo el `sent_at` original, lo que
   permite al emisor medir el viaje de ida y vuelta sin mantener estado por
   sonda. El costo del enlace es la mitad de ese tiempo, es decir el retardo en
   un sentido.
2. **Anuncio.** El nodo construye un *link state packet* con su identificador,
   un número de secuencia y el conjunto de sus vecinos con sus costos. Solo
   anuncia lo que puede medir directamente, que es la propiedad que define a un
   protocolo de estado de enlace.
3. **Difusión.** El LSP se disemina con la función `Flood`, la misma que utiliza
   el modo flooding.
4. **Base de datos.** Cada nodo almacena el anuncio más reciente de cada origen.
   Un anuncio con secuencia menor o igual a la almacenada se descarta y no se
   redifunde; este es el mecanismo que hace converger la difusión.
5. **Cálculo.** Con la topología reconstruida a partir de la base de datos se
   invoca `Dijkstra`, obteniendo la tabla de forwarding.
6. **Envejecimiento.** Los anuncios se refrescan periódicamente. Un anuncio que
   deja de refrescarse expira y su origen se elimina de la topología, que es
   como se detecta la caída de un nodo no adyacente.

#### Control del tráfico de señalización

Durante las pruebas se detectó un defecto de diseño que solo se manifiesta con
la red completa en ejecución. Los costos de enlace provienen de mediciones
reales, y en un enlace rápido el tiempo de ida y vuelta varía más del 50 % entre
una sonda y la siguiente. Como la condición inicial para reanunciar era un
cambio relativo del 25 %, cada nodo reanunciaba su estado en cada `hello`,
generando una tormenta de paquetes de control que crece con el tamaño de la red.

Se corrigió con dos mecanismos:

1. **Suavizado.** El plano de descubrimiento mantiene un promedio móvil
   exponencial del tiempo de ida y vuelta de cada enlace, de modo que una
   muestra ruidosa no altera significativamente el costo.
2. **Política de anuncio.** La aparición o desaparición de un vecino es un
   cambio de topología y se difunde de inmediato. Un costo que solo fluctuó se
   anuncia únicamente si superó un umbral y nunca con más frecuencia que el
   límite de tasa establecido; el refresco periódico lo transporta de todos
   modos.

El efecto se midió sobre la red de nueve nodos en contenedores y se documenta en
la sección de resultados.

---

## 3. Resultados

### 3.1 Entorno de pruebas

Las pruebas se ejecutaron en tres entornos:

1. **Suite automatizada.** Pruebas unitarias de los algoritmos y pruebas de
   integración que levantan nodos reales sobre sockets de loopback. Se ejecutan
   con `make test` y bajo el detector de condiciones de carrera con `make race`.
2. **Nueve procesos locales**, uno por nodo, sobre la topología de referencia.
3. **Nueve contenedores Docker**, uno por nodo, sobre una red bridge privada.
   Cada nodo tiene su propia pila de red y su propia dirección, lo que se
   aproxima a la prueba en clase.

Topología de referencia utilizada:

```
A — B — D — E
|  /|   |   |
| / |   |   |
C   |   F — G — I
 \  |  /|      /
  \ | / |     /
    ·   H ———
```

Adyacencias: A(B,C) B(A,C,D) C(A,B,F) D(B,E,F) E(D,G) F(C,D,G,H) G(E,F,I)
H(F,I) I(G,H).

### 3.2 Convergencia y enrutamiento en LSR

Partiendo de nodos que solo conocen a sus vecinos, la red converge y el tráfico
sigue el camino de menor costo:

```
[I] MESSAGE from A (path A>C>F>G): hola-multi-hop
```

El nodo A nunca fue informado de la existencia de I; aprendió la ruta
exclusivamente a partir de los anuncios difundidos.

### 3.3 Entrega única bajo flooding

Sobre la misma topología, que contiene ciclos, un mensaje enviado en modo
flooding se entrega exactamente una vez:

```
[I] MESSAGE from A (path A>B>D>E>G): flood-test
```

La prueba de integración `TestFloodingDeliversExactlyOnceDespiteACycle` verifica
además que no llegue una segunda copia dentro de una ventana de espera.

### 3.4 Reacción ante la caída de un router

Se detuvo el contenedor del nodo F, que formaba parte de la ruta activa hacia I,
y se reenvió el mensaje tras la convergencia:

| Momento | Ruta observada | Siguiente salto desde A |
| --- | --- | --- |
| Antes de la falla | `A>C>F>H` | C |
| Después de la falla | `A>B>D>E>G` | B |

El nodo A registró la retirada de los enlaces hacia F por parte de C, D, G y H,
y posteriormente la expiración del anuncio del propio F:

```
[A] topology update: C is linked to [A B]
[A] topology update: D is linked to [B E]
[A] topology update: G is linked to [E I]
[A] link-state packet of F expired, removing it from the topology
```

Al reiniciar el contenedor, F fue reincorporado a la topología.

### 3.5 Tráfico de control

Medición del número de secuencia alcanzado por los anuncios, como indicador
directo del volumen de señalización:

| Versión | Tiempo de ejecución | Secuencia máxima observada |
| --- | --- | --- |
| Antes de la corrección | 60 s | 85 |
| Después de la corrección | 90 s | 12 |

Con un refresco periódico de 8 segundos, 90 segundos de ejecución corresponden a
aproximadamente 11 anuncios por nodo. Es decir, tras la corrección el tráfico de
control equivale al refresco diseñado y nada más.

Estado de la base de datos de enlaces del nodo A tras la corrección:

```
B  seq=11   A(0.13) C(0.12) D(0.12)
C  seq=11   A(0.14) B(0.10) F(0.09)
D  seq=11   B(0.11) E(0.14) F(0.13)
E  seq=10   D(0.11) G(0.11)
F  seq=12   C(0.11) D(0.12) G(0.12) H(0.12)
G  seq=11   E(0.11) F(0.11) I(0.11)
H  seq=10   F(0.17) I(0.15)
I  seq=10   G(0.16) H(0.12)
```

### 3.6 Interconexión en clase

La prueba conjunta usó una red de nueve grupos, identificados `A`–`I`, sobre la
red inalámbrica del salón. Cada grupo corrió su propio nodo en modo `lsr` y
solo configuró la fila correspondiente a su propio id: ni la topología completa
ni las direcciones de los demás grupos son necesarias para que LSR funcione,
ya que la topología se termina de aprender por difusión de anuncios de estado
de enlace (ver 2.6).

**Direcciones IP asignadas.** Cada grupo comunicó la IP en la que su nodo
escucha (puerto 5000 para todos). Quedaron registradas en el pizarrón
(Figura 1) y configuradas en `configs/names-class.json`:

| Grupo | Dirección |
| --- | --- |
| A | 192.168.0.60:5000 |
| B (nuestro grupo) | 192.168.0.128:5000 |
| C | 192.168.0.137:5000 |
| D | 192.168.0.155:5000 |
| E | 192.168.0.219:5000 |
| F | 192.168.0.153:5000 |
| G | 192.168.0.218:5000 |
| H | 192.168.0.138:5000 |
| I | 192.168.0.59:5000 |

![Direcciones IP asignadas a cada grupo](IMG_2615.JPG)

**Topología acordada.** La cátedra proyectó el mapa de conexiones de referencia
para los nueve grupos (Figura 2), con el costo de cada enlace ya fijado:

![Mapa de conexiones entre nodos proyectado por la cátedra](conexion.jpeg)

A cada grupo le correspondía configurar únicamente su propia fila de esa
topología. Al grupo B le tocaron los vecinos A, C y E, con costo 4, 1 y 5
respectivamente (`configs/topo-class.json`) — exactamente la adyacencia de B en
la Figura 2. El grafo completo (`configs/topo-weighted.json`, transcrito de la
misma figura para poder correr el modo `dijkstra` de forma aislada) es:

| Nodo | Vecinos (costo) |
| --- | --- |
| A | B(4), C(2), D(7) |
| B | A(4), C(1), E(5) |
| C | A(2), B(1), D(3), F(8) |
| D | A(7), C(3), F(2), G(6) |
| E | B(5), F(3), H(6) |
| F | C(8), D(2), E(3), G(1), H(4) |
| G | D(6), F(1), I(6) |
| H | E(6), F(4), I(2) |
| I | G(6), H(2) |

En modo `lsr` (el usado en la prueba) ningún grupo necesitó conocer esta tabla
completa: cada uno solo configuró su propia fila, y el resto se aprendió por
difusión de anuncios de estado de enlace, que es precisamente la propiedad que
LSR explota.

**Ajustes al protocolo negociados entre equipos.** El formato base sugerido por
la cátedra (sección 3.2 del enunciado) se refinó entre grupos hasta la versión
descrita en 2.3: `from`/`to` como `IP:puerto`, `version`, `checksum` CRC32 del
payload canónico, y los encabezados `via`/`t0`/`trace`. Nuestra implementación
inicial usaba nombres distintos para varios de estos campos (id corto en vez de
dirección, `hop`/`sent_at`/`path`, sin `checksum` ni `version`); se corrigió el
mismo día de la prueba para cumplir el acuerdo final, aislando la traducción
id↔dirección en la frontera de red para no tocar los algoritmos de ruteo.

**Resultados de la interconexión.** Como verificación cruzada, el grupo
consolidó en el pizarrón la tabla de enrutamiento completa —origen, destino,
siguiente salto y costo— resultante de la red de nueve nodos ya convergida
(Figura 3). Se usó para confirmar que las tablas calculadas por cada
implementación coincidían entre sí, es decir, que distintas implementaciones de
LSR sobre la misma topología llegan al mismo resultado.

![Tabla de enrutamiento consolidada (origen/destino, siguiente salto y costo) tras la convergencia de los nueve grupos](resultados.jpeg)

> _(Foto de pizarrón; letra manuscrita. Antes de entregar, verificar con el
> grupo que la transcripción de cualquier celda citada en el texto coincide con
> la imagen.)_

**Incompatibilidades encontradas.** La principal fue de formato, no de
comportamiento: antes de acordar el protocolo final, nuestro nodo enviaba
`from`/`to` como el id corto de topología en vez de la dirección IP, por lo que
un nodo de otro equipo no podía interpretar a quién iba dirigido un paquete
nuestro. Se resolvió adoptando `IP:puerto` en el borde de red, sin cambiar la
lógica interna de enrutamiento (sección 2.3).

**Tiempo de convergencia.** _(completar con el tiempo observado desde que se
levantaron los nueve nodos hasta que `topology`/`dijkstra` mostraron rutas a
los nueve grupos)._

---

## 4. Discusión

### 4.1 Comparación entre los algoritmos

| | Información requerida | Tabla | Tráfico de datos | Reacción a fallas |
| --- | --- | --- | --- | --- |
| Dijkstra | Topología completa | Calculada al arrancar | Mínimo, una copia por camino | Poda vecinos caídos |
| Flooding | Solo vecinos | Ninguna | Alto, crece con las aristas | Deja de usar el enlace mudo |
| LSR | Vecinos, luego topología aprendida | Recalculada ante cambios | Mínimo en datos, moderado en control | Reanuncia, envejece y recalcula |

Flooding es el único que funciona sin ninguna información previa, y por eso es
el mecanismo de arranque de LSR: antes de conocer la topología no existe forma
de dirigir un anuncio hacia un destino específico. Su costo es que el tráfico de
datos crece con el número de aristas, lo que lo hace inviable como algoritmo de
red general.

Dijkstra produce el enrutamiento óptimo con el mínimo tráfico, pero exige que
alguien le entregue la topología completa, lo cual no es realista en una red que
cambia.

LSR resuelve esa tensión: usa flooding solo para el tráfico de control, que es
acotado y periódico, y usa Dijkstra sobre la topología resultante para el
tráfico de datos, que es el volumen realmente significativo.

### 4.2 Sobre la modularidad

La decisión de implementar Dijkstra y Flooding como funciones puras tuvo un
efecto verificable: las pruebas unitarias de ambos algoritmos no requieren
sockets, procesos ni temporizadores, y las pruebas de LSR ejercitan exactamente
el mismo código que se ejecuta en producción. Si se hubieran escrito como dos
programas separados con lógica duplicada, cualquier corrección en uno habría
tenido que replicarse manualmente en el otro.

### 4.3 Sobre la medición de costos

Medir el costo de un enlace con el tiempo real de ida y vuelta es más realista
que asignar pesos fijos, pero introduce el problema descrito en la sección 2.6:
la señalización pasa a depender del ruido de medición. Esta es una diferencia
importante entre implementar un algoritmo sobre un grafo estático y operarlo
sobre una red real, y no se manifestó en las pruebas unitarias sino únicamente
al ejecutar la red completa.

### 4.4 Limitaciones

- El TTL por defecto es de 8 saltos. En una red mayor que la del laboratorio
  habría que aumentarlo.
- Los costos se miden en milisegundos de retardo; no se considera ancho de banda
  ni pérdida de paquetes.
- No se implementó autenticación de los anuncios: un nodo malicioso podría
  anunciar enlaces inexistentes y atraer tráfico.
- La detección de caída depende de que el vecino deje de responder. Un enlace
  que responde pero descarta datos no se detecta.

---

## 5. Conclusiones

1. Los tres algoritmos se implementaron y verificaron sobre una red de nueve
   nodos, tanto en procesos locales como en contenedores independientes.
2. Tratar Dijkstra y Flooding como funciones puras, y no como programas
   separados, permitió que LSR los reutilizara literalmente. Existe una sola
   implementación de cada algoritmo en el proyecto.
3. La terminación del flooding depende de la detección de duplicados, no del
   TTL. El TTL es una protección de último recurso frente a implementaciones
   externas incorrectas.
4. LSR converge desde el desconocimiento total de la topología y se recupera de
   la caída de un router intermedio sin intervención manual.
5. El problema de mayor impacto encontrado no fue algorítmico sino operativo: el
   ruido de las mediciones de costo generaba una tormenta de señalización. Solo
   se hizo evidente al ejecutar la red completa, lo que muestra que las pruebas
   unitarias son necesarias pero no suficientes para un sistema distribuido.

---

## 6. Comentarios

_(Espacio para observaciones del grupo sobre la práctica, dificultades
encontradas y sugerencias.)_

---

## 7. Referencias

1. Tanenbaum, A. S., & Wetherall, D. J. (2011). *Computer Networks* (5.ª ed.).
   Prentice Hall. Capítulo 5: The Network Layer.
2. Kurose, J. F., & Ross, K. W. (2016). *Computer Networking: A Top-Down
   Approach* (7.ª ed.). Pearson. Capítulo 5: The Network Layer — Control Plane.
3. Dijkstra, E. W. (1959). A note on two problems in connexion with graphs.
   *Numerische Mathematik*, 1(1), 269–271.
4. Moy, J. (1998). *OSPF Version 2* (RFC 2328). Internet Engineering Task Force.
   https://www.rfc-editor.org/rfc/rfc2328
5. The Go Authors. *The Go Programming Language Specification*.
   https://go.dev/ref/spec
