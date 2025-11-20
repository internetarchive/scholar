package fatcat2

import (
	"testing"
)

func Test_File_SetMetadata(t *testing.T) {

	f := &File{}

	bs := []byte(sample)
	err := f.SetMetadata(bs)
	if err != nil {
		t.Errorf("did not expect error but got '%s'", err.Error())
	}

	if f.Size != len(bs) {
		t.Errorf("expected size %d, got %d", len(bs), f.Size)
	}

	expectedMd5 := "89f73763f13a6200d1ad29a85d82fde9"
	if f.Md5 != expectedMd5 {
		t.Errorf("expected md5 %s, got %s", expectedMd5, f.Md5)
	}

	expectedSha1 := "755c8252201ac4fa37ce4beb9dd1063abbea7985"
	if f.Sha1 != expectedSha1 {
		t.Errorf("expected sha1 %s, got %s", expectedSha1, f.Sha1)
	}

	expectedSha256 := "cb5b552624a1ee8ccc2e46e1950e8bc6972de03807ceeef229541c38ae3fe7c0"
	if f.Sha256 != expectedSha256 {
		t.Errorf("expected sha256 %s, got %s", expectedSha256, f.Sha256)
	}
}

const sample = `
A PIECE OF COFFEE.

More of double.

A place in no new table.

A single image is not splendor. Dirty is yellow. A sign of more in not
mentioned. A piece of coffee is not a detainer. The resemblance to
yellow is dirtier and distincter. The clean mixture is whiter and not
coal color, never more coal color than altogether.

The sight of a reason, the same sight slighter, the sight of a simpler
negative answer, the same sore sounder, the intention to wishing, the
same splendor, the same furniture.

The time to show a message is when too late and later there is no
hanging in a blight.

A not torn rose-wood color. If it is not dangerous then a pleasure and
more than any other if it is cheap is not cheaper. The amusing side is
that the sooner there are no fewer the more certain is the necessity
dwindled. Supposing that the case contained rose-wood and a color.
Supposing that there was no reason for a distress and more likely for a
number, supposing that there was no astonishment, is it not necessary to
mingle astonishment.

The settling of stationing cleaning is one way not to shatter scatter
and scattering. The one way to use custom is to use soap and silk for
cleaning. The one way to see cotton is to have a design concentrating
the illusion and the illustration. The perfect way is to accustom the
thing to have a lining and the shape of a ribbon and to be solid, quite
solid in standing and to use heaviness in morning. It is light enough in
that. It has that shape nicely. Very nicely may not be exaggerating.
Very strongly may be sincerely fainting. May be strangely flattering.
May not be strange in everything. May not be strange to.
`
