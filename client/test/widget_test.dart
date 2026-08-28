import 'package:flutter_test/flutter_test.dart';
import 'package:swadrive/main.dart';

void main() {
  testWidgets('scaffold renders the placeholder screen', (tester) async {
    await tester.pumpWidget(const MainApp());

    expect(find.text('Hello World!'), findsOneWidget);
  });
}
